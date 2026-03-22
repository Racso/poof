package store

import (
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

type Store struct {
	db *sql.DB
}

type Project struct {
	Name      string    `json:"name"`
	Domain    string    `json:"domain"`
	Image     string    `json:"image"`
	Repo      string    `json:"repo"`
	Branch    string    `json:"branch"`
	Port      int       `json:"port"`
	Token     string    `json:"token"`
	Subpath   string    `json:"subpath"`
	CreatedAt time.Time `json:"created_at"`
}

type Deployment struct {
	ID         int64     `json:"id"`
	Project    string    `json:"project"`
	Image      string    `json:"image"`
	Status     string    `json:"status"`
	DeployedAt time.Time `json:"deployed_at"`
}

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return s, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) migrate() error {
	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS projects (
			name        TEXT PRIMARY KEY,
			domain      TEXT NOT NULL,
			image       TEXT NOT NULL,
			repo        TEXT NOT NULL,
			branch      TEXT NOT NULL DEFAULT 'main',
			port        INTEGER NOT NULL DEFAULT 8080,
			token       TEXT NOT NULL,
			subpath     TEXT NOT NULL DEFAULT 'disabled',
			created_at  DATETIME DEFAULT CURRENT_TIMESTAMP
		);

		CREATE TABLE IF NOT EXISTS deployments (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			project     TEXT NOT NULL,
			image       TEXT NOT NULL,
			status      TEXT NOT NULL DEFAULT 'success',
			deployed_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			FOREIGN KEY (project) REFERENCES projects(name) ON DELETE CASCADE
		);

		CREATE TABLE IF NOT EXISTS env_vars (
			project TEXT NOT NULL,
			key     TEXT NOT NULL,
			value   TEXT NOT NULL,
			PRIMARY KEY (project, key),
			FOREIGN KEY (project) REFERENCES projects(name) ON DELETE CASCADE
		);

		PRAGMA foreign_keys = ON;
	`)
	if err != nil {
		return err
	}
	// v2: add subpath column to existing databases (error = column already exists, safe to ignore).
	s.db.Exec(`ALTER TABLE projects ADD COLUMN subpath TEXT NOT NULL DEFAULT 'disabled'`)
	return nil
}

// --- Projects ---

func (s *Store) CreateProject(p Project) error {
	_, err := s.db.Exec(
		`INSERT INTO projects (name, domain, image, repo, branch, port, token, subpath)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		p.Name, p.Domain, p.Image, p.Repo, p.Branch, p.Port, p.Token, p.Subpath,
	)
	if err != nil {
		return fmt.Errorf("create project: %w", err)
	}
	return nil
}

func (s *Store) GetProject(name string) (*Project, error) {
	p := &Project{}
	err := s.db.QueryRow(
		`SELECT name, domain, image, repo, branch, port, token, subpath, created_at
		 FROM projects WHERE name = ?`, name,
	).Scan(&p.Name, &p.Domain, &p.Image, &p.Repo, &p.Branch, &p.Port, &p.Token, &p.Subpath, &p.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get project: %w", err)
	}
	return p, nil
}

func (s *Store) ListProjects() ([]Project, error) {
	rows, err := s.db.Query(
		`SELECT name, domain, image, repo, branch, port, token, subpath, created_at
		 FROM projects ORDER BY name`,
	)
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	defer rows.Close()

	var projects []Project
	for rows.Next() {
		var p Project
		if err := rows.Scan(&p.Name, &p.Domain, &p.Image, &p.Repo, &p.Branch, &p.Port, &p.Token, &p.Subpath, &p.CreatedAt); err != nil {
			return nil, err
		}
		projects = append(projects, p)
	}
	return projects, rows.Err()
}

func (s *Store) UpdateProject(p Project) error {
	_, err := s.db.Exec(
		`UPDATE projects SET domain=?, image=?, repo=?, branch=?, port=?, subpath=? WHERE name=?`,
		p.Domain, p.Image, p.Repo, p.Branch, p.Port, p.Subpath, p.Name,
	)
	return err
}

func (s *Store) DeleteProject(name string) error {
	_, err := s.db.Exec(`DELETE FROM projects WHERE name = ?`, name)
	return err
}

// --- Deployments ---

func (s *Store) RecordDeployment(project, image, status string) (int64, error) {
	res, err := s.db.Exec(
		`INSERT INTO deployments (project, image, status) VALUES (?, ?, ?)`,
		project, image, status,
	)
	if err != nil {
		return 0, fmt.Errorf("record deployment: %w", err)
	}
	return res.LastInsertId()
}

func (s *Store) UpdateDeploymentStatus(id int64, status string) error {
	_, err := s.db.Exec(`UPDATE deployments SET status = ? WHERE id = ?`, status, id)
	return err
}

// LastDeployment returns the most recent deployment for a project.
func (s *Store) LastDeployment(project string) (*Deployment, error) {
	d := &Deployment{}
	err := s.db.QueryRow(
		`SELECT id, project, image, status, deployed_at
		 FROM deployments WHERE project = ? ORDER BY id DESC LIMIT 1`, project,
	).Scan(&d.ID, &d.Project, &d.Image, &d.Status, &d.DeployedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("last deployment: %w", err)
	}
	return d, nil
}

// PreviousDeployment returns the second-to-last successful deployment (for rollback).
// Uses ORDER BY id DESC (not deployed_at) because SQLite timestamps have second
// resolution and multiple deployments in the same second would be non-deterministic.
func (s *Store) PreviousDeployment(project string) (*Deployment, error) {
	d := &Deployment{}
	err := s.db.QueryRow(
		`SELECT id, project, image, status, deployed_at
		 FROM deployments WHERE project = ? AND status = 'success'
		 ORDER BY id DESC LIMIT 1 OFFSET 1`, project,
	).Scan(&d.ID, &d.Project, &d.Image, &d.Status, &d.DeployedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("previous deployment: %w", err)
	}
	return d, nil
}

func (s *Store) ListDeployments(project string, limit int) ([]Deployment, error) {
	rows, err := s.db.Query(
		`SELECT id, project, image, status, deployed_at
		 FROM deployments WHERE project = ?
		 ORDER BY deployed_at DESC LIMIT ?`, project, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("list deployments: %w", err)
	}
	defer rows.Close()

	var deployments []Deployment
	for rows.Next() {
		var d Deployment
		if err := rows.Scan(&d.ID, &d.Project, &d.Image, &d.Status, &d.DeployedAt); err != nil {
			return nil, err
		}
		deployments = append(deployments, d)
	}
	return deployments, rows.Err()
}

// --- Env Vars ---

func (s *Store) SetEnvVar(project, key, value string) error {
	_, err := s.db.Exec(
		`INSERT INTO env_vars (project, key, value) VALUES (?, ?, ?)
		 ON CONFLICT(project, key) DO UPDATE SET value = excluded.value`,
		project, key, value,
	)
	return err
}

func (s *Store) UnsetEnvVar(project, key string) error {
	_, err := s.db.Exec(`DELETE FROM env_vars WHERE project = ? AND key = ?`, project, key)
	return err
}

func (s *Store) GetEnvVars(project string) (map[string]string, error) {
	rows, err := s.db.Query(`SELECT key, value FROM env_vars WHERE project = ?`, project)
	if err != nil {
		return nil, fmt.Errorf("get env vars: %w", err)
	}
	defer rows.Close()

	vars := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		vars[k] = v
	}
	return vars, rows.Err()
}
