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
	Folder    string    `json:"folder"`
	CreatedAt time.Time `json:"created_at"`
}

type Volume struct {
	ID            int64     `json:"id"`
	Project       string    `json:"project"`
	HostPath      string    `json:"host_path"`
	ContainerPath string    `json:"container_path"`
	Managed       bool      `json:"managed"`
	CreatedAt     time.Time `json:"created_at"`
}

type Redirect struct {
	ID         int64     `json:"id"`
	FromDomain string    `json:"from"`
	ToDomain   string    `json:"to"`
	CreatedAt  time.Time `json:"created_at"`
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
			branch      TEXT NOT NULL,
			port        INTEGER NOT NULL,
			token       TEXT NOT NULL,
			subpath     TEXT NOT NULL,
			folder      TEXT NOT NULL DEFAULT '',
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

		CREATE TABLE IF NOT EXISTS redirects (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			from_domain TEXT NOT NULL UNIQUE,
			to_domain   TEXT NOT NULL,
			created_at  DATETIME DEFAULT CURRENT_TIMESTAMP
		);

		CREATE TABLE IF NOT EXISTS volumes (
			id             INTEGER PRIMARY KEY AUTOINCREMENT,
			project        TEXT NOT NULL,
			host_path      TEXT NOT NULL,
			container_path TEXT NOT NULL,
			managed        BOOLEAN NOT NULL DEFAULT 0,
			created_at     DATETIME DEFAULT CURRENT_TIMESTAMP,
			FOREIGN KEY (project) REFERENCES projects(name) ON DELETE CASCADE
		);

		PRAGMA foreign_keys = ON;
	`)
	if err != nil {
		return err
	}
	// Add folder column to existing databases that predate this field.
	// Ignore the error — it fires on "duplicate column name" when already present.
	s.db.Exec(`ALTER TABLE projects ADD COLUMN folder TEXT NOT NULL DEFAULT ''`)
	return nil
}

// --- Projects ---

func (s *Store) CreateProject(p Project) error {
	_, err := s.db.Exec(
		`INSERT INTO projects (name, domain, image, repo, branch, port, token, subpath, folder)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.Name, p.Domain, p.Image, p.Repo, p.Branch, p.Port, p.Token, p.Subpath, p.Folder,
	)
	if err != nil {
		return fmt.Errorf("create project: %w", err)
	}
	return nil
}

func (s *Store) GetProject(name string) (*Project, error) {
	p := &Project{}
	err := s.db.QueryRow(
		`SELECT name, domain, image, repo, branch, port, token, subpath, folder, created_at
		 FROM projects WHERE name = ?`, name,
	).Scan(&p.Name, &p.Domain, &p.Image, &p.Repo, &p.Branch, &p.Port, &p.Token, &p.Subpath, &p.Folder, &p.CreatedAt)
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
		`SELECT name, domain, image, repo, branch, port, token, subpath, folder, created_at
		 FROM projects ORDER BY name`,
	)
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	defer rows.Close()

	var projects []Project
	for rows.Next() {
		var p Project
		if err := rows.Scan(&p.Name, &p.Domain, &p.Image, &p.Repo, &p.Branch, &p.Port, &p.Token, &p.Subpath, &p.Folder, &p.CreatedAt); err != nil {
			return nil, err
		}
		projects = append(projects, p)
	}
	return projects, rows.Err()
}

func (s *Store) UpdateProject(p Project) error {
	_, err := s.db.Exec(
		`UPDATE projects SET domain=?, image=?, repo=?, branch=?, port=?, subpath=?, folder=? WHERE name=?`,
		p.Domain, p.Image, p.Repo, p.Branch, p.Port, p.Subpath, p.Folder, p.Name,
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

// --- Redirects ---

func (s *Store) CreateRedirect(from, to string) (*Redirect, error) {
	res, err := s.db.Exec(
		`INSERT INTO redirects (from_domain, to_domain) VALUES (?, ?)`,
		from, to,
	)
	if err != nil {
		return nil, fmt.Errorf("create redirect: %w", err)
	}
	id, _ := res.LastInsertId()
	return s.GetRedirect(id)
}

func (s *Store) GetRedirect(id int64) (*Redirect, error) {
	r := &Redirect{}
	err := s.db.QueryRow(
		`SELECT id, from_domain, to_domain, created_at FROM redirects WHERE id = ?`, id,
	).Scan(&r.ID, &r.FromDomain, &r.ToDomain, &r.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get redirect: %w", err)
	}
	return r, nil
}

func (s *Store) ListRedirects() ([]Redirect, error) {
	rows, err := s.db.Query(
		`SELECT id, from_domain, to_domain, created_at FROM redirects ORDER BY id`,
	)
	if err != nil {
		return nil, fmt.Errorf("list redirects: %w", err)
	}
	defer rows.Close()

	var redirects []Redirect
	for rows.Next() {
		var r Redirect
		if err := rows.Scan(&r.ID, &r.FromDomain, &r.ToDomain, &r.CreatedAt); err != nil {
			return nil, err
		}
		redirects = append(redirects, r)
	}
	return redirects, rows.Err()
}

func (s *Store) DeleteRedirect(id int64) (bool, error) {
	res, err := s.db.Exec(`DELETE FROM redirects WHERE id = ?`, id)
	if err != nil {
		return false, fmt.Errorf("delete redirect: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// --- Volumes ---

func (s *Store) CreateVolume(v Volume) (*Volume, error) {
	res, err := s.db.Exec(
		`INSERT INTO volumes (project, host_path, container_path, managed) VALUES (?, ?, ?, ?)`,
		v.Project, v.HostPath, v.ContainerPath, v.Managed,
	)
	if err != nil {
		return nil, fmt.Errorf("create volume: %w", err)
	}
	id, _ := res.LastInsertId()
	return s.GetVolume(id)
}

func (s *Store) GetVolume(id int64) (*Volume, error) {
	v := &Volume{}
	err := s.db.QueryRow(
		`SELECT id, project, host_path, container_path, managed, created_at FROM volumes WHERE id = ?`, id,
	).Scan(&v.ID, &v.Project, &v.HostPath, &v.ContainerPath, &v.Managed, &v.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get volume: %w", err)
	}
	return v, nil
}

func (s *Store) ListVolumes(project string) ([]Volume, error) {
	rows, err := s.db.Query(
		`SELECT id, project, host_path, container_path, managed, created_at FROM volumes WHERE project = ? ORDER BY id`,
		project,
	)
	if err != nil {
		return nil, fmt.Errorf("list volumes: %w", err)
	}
	defer rows.Close()

	var volumes []Volume
	for rows.Next() {
		var v Volume
		if err := rows.Scan(&v.ID, &v.Project, &v.HostPath, &v.ContainerPath, &v.Managed, &v.CreatedAt); err != nil {
			return nil, err
		}
		volumes = append(volumes, v)
	}
	return volumes, rows.Err()
}

func (s *Store) DeleteVolume(id int64) (bool, error) {
	res, err := s.db.Exec(`DELETE FROM volumes WHERE id = ?`, id)
	if err != nil {
		return false, fmt.Errorf("delete volume: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}
