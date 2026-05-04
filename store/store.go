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

// CI modes select the shape of the GitHub Actions workflow Poof! commits.
//   - managed:  the workflow runs on its own (push-triggered); current default.
//   - callable: the workflow is a reusable workflow (on: workflow_call),
//     meant to be invoked from a user-owned outer workflow that adds
//     surrounding steps (tests, lint, matrix builds, etc.).
// CIMode is irrelevant when CI is false; persisted defaults to "managed".
const (
	CIModeManaged  = "managed"
	CIModeCallable = "callable"
)

type Project struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	Domain    string    `json:"domain"`
	Image     string    `json:"image"`
	Repo      string    `json:"repo"`
	Branch    string    `json:"branch"`
	Port      int       `json:"port"`
	Subpath   string    `json:"subpath"`
	Folder    string    `json:"folder"`
	Static    string    `json:"static"`
	Build     bool      `json:"build"`
	CI        bool      `json:"ci"`
	CIMode    string    `json:"ci_mode"`
	CreatedAt time.Time `json:"created_at"`
}

// IsStatic returns true if the project is a static site or SPA.
func (p Project) IsStatic() bool {
	return p.Static == "static" || p.Static == "spa"
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
	ProjectID  int64     `json:"project_id"`
	Project    string    `json:"project"`
	Image      string    `json:"image"`
	Status     string    `json:"status"`
	DeployedAt time.Time `json:"deployed_at"`
}

// GCPolicyGlobalKey is the project name used to store the global default GC
// policy that applies to projects without their own override.
const GCPolicyGlobalKey = "*"

// GCPolicy is the GC retention policy for one project (or the global default).
// KeepCount and OlderThanDays are nil when not set; Disabled=true means GC is
// explicitly turned off and the row exists only as an override.
type GCPolicy struct {
	Project       string `json:"project"`
	KeepCount     *int   `json:"keep_count,omitempty"`
	OlderThanDays *int   `json:"older_than_days,omitempty"`
	Disabled      bool   `json:"disabled,omitempty"`
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
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			name       TEXT UNIQUE NOT NULL,
			domain     TEXT NOT NULL,
			image      TEXT NOT NULL,
			repo       TEXT NOT NULL,
			branch     TEXT NOT NULL,
			port       INTEGER NOT NULL,
			token      TEXT NOT NULL,
			subpath    TEXT NOT NULL,
			folder     TEXT NOT NULL DEFAULT '',
			static     TEXT NOT NULL DEFAULT '',
			build      INTEGER NOT NULL DEFAULT 0,
			ci         INTEGER NOT NULL DEFAULT 1,
			ci_mode    TEXT NOT NULL DEFAULT 'managed',
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		);

		CREATE TABLE IF NOT EXISTS deployments (
			id          INTEGER PRIMARY KEY AUTOINCREMENT,
			project_id  INTEGER NOT NULL,
			image       TEXT NOT NULL,
			status      TEXT NOT NULL DEFAULT 'success',
			deployed_at DATETIME DEFAULT CURRENT_TIMESTAMP
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

		CREATE TABLE IF NOT EXISTS settings (
			key   TEXT PRIMARY KEY,
			value TEXT NOT NULL
		);

		CREATE TABLE IF NOT EXISTS repo_tokens (
			repo  TEXT PRIMARY KEY,
			token TEXT NOT NULL
		);

		CREATE TABLE IF NOT EXISTS caddy_snippets (
			project TEXT PRIMARY KEY,
			content TEXT NOT NULL,
			FOREIGN KEY (project) REFERENCES projects(name) ON DELETE CASCADE
		);

		CREATE TABLE IF NOT EXISTS gc_policies (
			project         TEXT PRIMARY KEY,
			keep_count      INTEGER,
			older_than_days INTEGER,
			disabled        INTEGER NOT NULL DEFAULT 0
		);
	`)
	if err != nil {
		return err
	}

	// Incremental column migrations for pre-existing databases.
	s.db.Exec(`ALTER TABLE projects ADD COLUMN folder TEXT NOT NULL DEFAULT ''`)
	s.db.Exec(`INSERT OR IGNORE INTO repo_tokens (repo, token)
		SELECT repo, token FROM projects WHERE token != '' GROUP BY repo`)
	s.db.Exec(`ALTER TABLE projects ADD COLUMN static TEXT NOT NULL DEFAULT ''`)
	s.db.Exec(`ALTER TABLE projects ADD COLUMN build INTEGER NOT NULL DEFAULT 0`)
	s.db.Exec(`ALTER TABLE projects ADD COLUMN ci INTEGER NOT NULL DEFAULT 1`)
	s.db.Exec(`ALTER TABLE projects ADD COLUMN ci_mode TEXT NOT NULL DEFAULT 'managed'`)

	// Structural migration: add project IDs, rewrite deployments table.
	if err := s.migrateProjectIDs(); err != nil {
		return fmt.Errorf("project-id migration: %w", err)
	}

	_, err = s.db.Exec(`PRAGMA foreign_keys = ON`)
	return err
}

// migrateProjectIDs converts the legacy schema (projects keyed by name,
// deployments referencing project by name with ON DELETE CASCADE) to the new
// schema (projects with autoincrement id, deployments referencing project_id,
// no cascade). This is a one-time, idempotent migration.
func (s *Store) migrateProjectIDs() error {
	// Detect old schema: deployments table has a TEXT 'project' column.
	var hasOldCol int
	if err := s.db.QueryRow(`
		SELECT COUNT(*) FROM pragma_table_info('deployments')
		WHERE name = 'project'
	`).Scan(&hasOldCol); err != nil {
		return fmt.Errorf("check schema: %w", err)
	}
	if hasOldCol == 0 {
		return nil // Fresh DB or already migrated.
	}

	// FK checks must be off to DROP and recreate referenced tables.
	if _, err := s.db.Exec(`PRAGMA foreign_keys = OFF`); err != nil {
		return fmt.Errorf("disable fk: %w", err)
	}

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback()

	// --- Recreate projects with autoincrement id ---
	if _, err := tx.Exec(`CREATE TABLE projects_new (
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		name       TEXT UNIQUE NOT NULL,
		domain     TEXT NOT NULL,
		image      TEXT NOT NULL,
		repo       TEXT NOT NULL,
		branch     TEXT NOT NULL,
		port       INTEGER NOT NULL,
		token      TEXT NOT NULL,
		subpath    TEXT NOT NULL,
		folder     TEXT NOT NULL DEFAULT '',
		static     TEXT NOT NULL DEFAULT '',
		build      INTEGER NOT NULL DEFAULT 0,
		ci         INTEGER NOT NULL DEFAULT 1,
		ci_mode    TEXT NOT NULL DEFAULT 'managed',
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	)`); err != nil {
		return fmt.Errorf("create projects_new: %w", err)
	}
	if _, err := tx.Exec(`
		INSERT INTO projects_new (name, domain, image, repo, branch, port, token, subpath, folder, static, build, ci, ci_mode, created_at)
		SELECT name, domain, image, repo, branch, port, token, subpath, folder, static, build, ci, ci_mode, created_at
		FROM projects
	`); err != nil {
		return fmt.Errorf("copy projects: %w", err)
	}
	if _, err := tx.Exec(`DROP TABLE projects`); err != nil {
		return fmt.Errorf("drop projects: %w", err)
	}
	if _, err := tx.Exec(`ALTER TABLE projects_new RENAME TO projects`); err != nil {
		return fmt.Errorf("rename projects: %w", err)
	}

	// --- Recreate deployments with project_id (no FK, no cascade) ---
	if _, err := tx.Exec(`CREATE TABLE deployments_new (
		id          INTEGER PRIMARY KEY AUTOINCREMENT,
		project_id  INTEGER NOT NULL,
		image       TEXT NOT NULL,
		status      TEXT NOT NULL DEFAULT 'success',
		deployed_at DATETIME DEFAULT CURRENT_TIMESTAMP
	)`); err != nil {
		return fmt.Errorf("create deployments_new: %w", err)
	}
	// Resolve project name → id. Orphan deployments (project already deleted
	// under the old CASCADE regime) are silently dropped.
	if _, err := tx.Exec(`
		INSERT INTO deployments_new (id, project_id, image, status, deployed_at)
		SELECT d.id, p.id, d.image, d.status, d.deployed_at
		FROM deployments d
		JOIN projects p ON d.project = p.name
	`); err != nil {
		return fmt.Errorf("copy deployments: %w", err)
	}
	if _, err := tx.Exec(`DROP TABLE deployments`); err != nil {
		return fmt.Errorf("drop deployments: %w", err)
	}
	if _, err := tx.Exec(`ALTER TABLE deployments_new RENAME TO deployments`); err != nil {
		return fmt.Errorf("rename deployments: %w", err)
	}

	return tx.Commit()
}

// --- Projects ---

func (s *Store) CreateProject(p Project) error {
	if p.CIMode == "" {
		p.CIMode = CIModeManaged
	}
	_, err := s.db.Exec(
		`INSERT INTO projects (name, domain, image, repo, branch, port, token, subpath, folder, static, build, ci, ci_mode)
		 VALUES (?, ?, ?, ?, ?, ?, '', ?, ?, ?, ?, ?, ?)`,
		p.Name, p.Domain, p.Image, p.Repo, p.Branch, p.Port, p.Subpath, p.Folder, p.Static, p.Build, p.CI, p.CIMode,
	)
	if err != nil {
		return fmt.Errorf("create project: %w", err)
	}
	return nil
}

func (s *Store) GetProject(name string) (*Project, error) {
	p := &Project{}
	err := s.db.QueryRow(
		`SELECT id, name, domain, image, repo, branch, port, subpath, folder, static, build, ci, ci_mode, created_at
		 FROM projects WHERE name = ?`, name,
	).Scan(&p.ID, &p.Name, &p.Domain, &p.Image, &p.Repo, &p.Branch, &p.Port, &p.Subpath, &p.Folder, &p.Static, &p.Build, &p.CI, &p.CIMode, &p.CreatedAt)
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
		`SELECT id, name, domain, image, repo, branch, port, subpath, folder, static, build, ci, ci_mode, created_at
		 FROM projects ORDER BY name`,
	)
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	defer rows.Close()

	var projects []Project
	for rows.Next() {
		var p Project
		if err := rows.Scan(&p.ID, &p.Name, &p.Domain, &p.Image, &p.Repo, &p.Branch, &p.Port, &p.Subpath, &p.Folder, &p.Static, &p.Build, &p.CI, &p.CIMode, &p.CreatedAt); err != nil {
			return nil, err
		}
		projects = append(projects, p)
	}
	return projects, rows.Err()
}

func (s *Store) GetRepoToken(repo string) (string, error) {
	var token string
	err := s.db.QueryRow(
		`SELECT token FROM repo_tokens WHERE repo = ?`, repo,
	).Scan(&token)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("get repo token: %w", err)
	}
	return token, nil
}

func (s *Store) SetRepoToken(repo, token string) error {
	_, err := s.db.Exec(
		`INSERT INTO repo_tokens (repo, token) VALUES (?, ?)
		 ON CONFLICT(repo) DO UPDATE SET token = excluded.token`,
		repo, token,
	)
	return err
}

func (s *Store) DeleteRepoToken(repo string) error {
	_, err := s.db.Exec(`DELETE FROM repo_tokens WHERE repo = ?`, repo)
	return err
}

func (s *Store) CountProjectsForRepo(repo string) (int, error) {
	var count int
	err := s.db.QueryRow(
		`SELECT COUNT(*) FROM projects WHERE repo = ?`, repo,
	).Scan(&count)
	return count, err
}

// CountCIEnabledProjectsForRepo counts projects in the same repo that have
// CI enabled, excluding the named project. Used to decide whether it's safe
// to remove shared repo secrets.
func (s *Store) CountCIEnabledProjectsForRepo(repo, excludeName string) (int, error) {
	var count int
	err := s.db.QueryRow(
		`SELECT COUNT(*) FROM projects WHERE repo = ? AND ci = 1 AND name != ?`, repo, excludeName,
	).Scan(&count)
	return count, err
}

func (s *Store) UpdateProject(p Project) error {
	if p.CIMode == "" {
		p.CIMode = CIModeManaged
	}
	_, err := s.db.Exec(
		`UPDATE projects SET domain=?, image=?, repo=?, branch=?, port=?, subpath=?, folder=?, static=?, build=?, ci=?, ci_mode=? WHERE name=?`,
		p.Domain, p.Image, p.Repo, p.Branch, p.Port, p.Subpath, p.Folder, p.Static, p.Build, p.CI, p.CIMode, p.Name,
	)
	return err
}

func (s *Store) DeleteProject(name string) error {
	_, err := s.db.Exec(`DELETE FROM projects WHERE name = ?`, name)
	return err
}

// --- Deployments ---

func (s *Store) RecordDeployment(project, image, status string) (int64, error) {
	var projectID int64
	if err := s.db.QueryRow(
		`SELECT id FROM projects WHERE name = ?`, project,
	).Scan(&projectID); err != nil {
		return 0, fmt.Errorf("record deployment: project %q: %w", project, err)
	}
	res, err := s.db.Exec(
		`INSERT INTO deployments (project_id, image, status) VALUES (?, ?, ?)`,
		projectID, image, status,
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
		`SELECT d.id, d.project_id, p.name, d.image, d.status, d.deployed_at
		 FROM deployments d
		 JOIN projects p ON d.project_id = p.id
		 WHERE p.name = ?
		 ORDER BY d.id DESC LIMIT 1`, project,
	).Scan(&d.ID, &d.ProjectID, &d.Project, &d.Image, &d.Status, &d.DeployedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("last deployment: %w", err)
	}
	return d, nil
}

// PreviousDeployment returns the second-to-last successful deployment (for rollback).
// Uses ORDER BY d.id DESC (not deployed_at) because SQLite timestamps have second
// resolution and multiple deployments in the same second would be non-deterministic.
func (s *Store) PreviousDeployment(project string) (*Deployment, error) {
	d := &Deployment{}
	err := s.db.QueryRow(
		`SELECT d.id, d.project_id, p.name, d.image, d.status, d.deployed_at
		 FROM deployments d
		 JOIN projects p ON d.project_id = p.id
		 WHERE p.name = ? AND d.status = 'success'
		 ORDER BY d.id DESC LIMIT 1 OFFSET 1`, project,
	).Scan(&d.ID, &d.ProjectID, &d.Project, &d.Image, &d.Status, &d.DeployedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("previous deployment: %w", err)
	}
	return d, nil
}

func (s *Store) ListDeployments(project string, limit int) ([]Deployment, error) {
	if limit <= 0 {
		limit = -1 // SQLite: -1 means no limit.
	}
	rows, err := s.db.Query(
		`SELECT d.id, d.project_id, p.name, d.image, d.status, d.deployed_at
		 FROM deployments d
		 JOIN projects p ON d.project_id = p.id
		 WHERE p.name = ?
		 ORDER BY d.deployed_at DESC LIMIT ?`, project, limit,
	)
	if err != nil {
		return nil, fmt.Errorf("list deployments: %w", err)
	}
	defer rows.Close()

	var deployments []Deployment
	for rows.Next() {
		var d Deployment
		if err := rows.Scan(&d.ID, &d.ProjectID, &d.Project, &d.Image, &d.Status, &d.DeployedAt); err != nil {
			return nil, err
		}
		deployments = append(deployments, d)
	}
	return deployments, rows.Err()
}

// ListOrphanDeploymentImages returns distinct Docker image refs from
// deployments whose project has been deleted or converted to static.
// These images are Poof-managed but no longer associated with an active
// container project — safe candidates for cleanup.
func (s *Store) ListOrphanDeploymentImages() ([]string, error) {
	rows, err := s.db.Query(`
		SELECT DISTINCT d.image FROM deployments d
		LEFT JOIN projects p ON d.project_id = p.id
		WHERE d.image != 'static'
		  AND (p.id IS NULL OR p.static != '')
	`)
	if err != nil {
		return nil, fmt.Errorf("list orphan images: %w", err)
	}
	defer rows.Close()

	var images []string
	for rows.Next() {
		var img string
		if err := rows.Scan(&img); err != nil {
			return nil, err
		}
		images = append(images, img)
	}
	return images, rows.Err()
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

// CopyEnvVars copies env vars from source to target. If keys is nil or
// contains "*", all vars are copied. Otherwise only the listed keys.
// Returns the list of keys that were actually copied.
func (s *Store) CopyEnvVars(source, target string, keys []string) ([]string, error) {
	vars, err := s.GetEnvVars(source)
	if err != nil {
		return nil, err
	}

	copyAll := len(keys) == 0 || (len(keys) == 1 && keys[0] == "*")
	if !copyAll {
		allowed := make(map[string]bool, len(keys))
		for _, k := range keys {
			allowed[k] = true
		}
		for k := range vars {
			if !allowed[k] {
				delete(vars, k)
			}
		}
	}

	copied := make([]string, 0, len(vars))
	for k, v := range vars {
		if err := s.SetEnvVar(target, k, v); err != nil {
			return nil, fmt.Errorf("copy env var %q: %w", k, err)
		}
		copied = append(copied, k)
	}
	return copied, nil
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

// --- Settings ---

func (s *Store) GetSetting(key string) (string, error) {
	var value string
	err := s.db.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&value)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return value, err
}

func (s *Store) SetSetting(key, value string) error {
	_, err := s.db.Exec(
		`INSERT INTO settings (key, value) VALUES (?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		key, value,
	)
	return err
}

func (s *Store) GetAllSettings() (map[string]string, error) {
	rows, err := s.db.Query(`SELECT key, value FROM settings ORDER BY key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	settings := make(map[string]string)
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		settings[k] = v
	}
	return settings, rows.Err()
}

// --- Caddy Snippets ---

func (s *Store) GetCaddySnippet(project string) (string, error) {
	var content string
	err := s.db.QueryRow(`SELECT content FROM caddy_snippets WHERE project = ?`, project).Scan(&content)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("get caddy snippet: %w", err)
	}
	return content, nil
}

func (s *Store) SetCaddySnippet(project, content string) error {
	_, err := s.db.Exec(
		`INSERT INTO caddy_snippets (project, content) VALUES (?, ?)
		 ON CONFLICT(project) DO UPDATE SET content = excluded.content`,
		project, content,
	)
	return err
}

func (s *Store) DeleteCaddySnippet(project string) (bool, error) {
	res, err := s.db.Exec(`DELETE FROM caddy_snippets WHERE project = ?`, project)
	if err != nil {
		return false, fmt.Errorf("delete caddy snippet: %w", err)
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

func (s *Store) GetAllCaddySnippets() (map[string]string, error) {
	rows, err := s.db.Query(`SELECT project, content FROM caddy_snippets ORDER BY project`)
	if err != nil {
		return nil, fmt.Errorf("list caddy snippets: %w", err)
	}
	defer rows.Close()
	snippets := make(map[string]string)
	for rows.Next() {
		var p, c string
		if err := rows.Scan(&p, &c); err != nil {
			return nil, err
		}
		snippets[p] = c
	}
	return snippets, rows.Err()
}

// --- GC Policies ---

func (s *Store) GetGCPolicy(project string) (*GCPolicy, error) {
	p := &GCPolicy{Project: project}
	var keep, older sql.NullInt64
	var disabled int
	err := s.db.QueryRow(
		`SELECT keep_count, older_than_days, disabled FROM gc_policies WHERE project = ?`,
		project,
	).Scan(&keep, &older, &disabled)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get gc policy: %w", err)
	}
	if keep.Valid {
		v := int(keep.Int64)
		p.KeepCount = &v
	}
	if older.Valid {
		v := int(older.Int64)
		p.OlderThanDays = &v
	}
	p.Disabled = disabled != 0
	return p, nil
}

func (s *Store) SetGCPolicy(p GCPolicy) error {
	var keep, older interface{}
	if p.KeepCount != nil {
		keep = *p.KeepCount
	}
	if p.OlderThanDays != nil {
		older = *p.OlderThanDays
	}
	disabled := 0
	if p.Disabled {
		disabled = 1
	}
	_, err := s.db.Exec(
		`INSERT INTO gc_policies (project, keep_count, older_than_days, disabled)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT(project) DO UPDATE SET
		   keep_count = excluded.keep_count,
		   older_than_days = excluded.older_than_days,
		   disabled = excluded.disabled`,
		p.Project, keep, older, disabled,
	)
	if err != nil {
		return fmt.Errorf("set gc policy: %w", err)
	}
	return nil
}

func (s *Store) DeleteGCPolicy(project string) error {
	_, err := s.db.Exec(`DELETE FROM gc_policies WHERE project = ?`, project)
	if err != nil {
		return fmt.Errorf("delete gc policy: %w", err)
	}
	return nil
}

func (s *Store) ListGCPolicies() ([]GCPolicy, error) {
	rows, err := s.db.Query(
		`SELECT project, keep_count, older_than_days, disabled FROM gc_policies ORDER BY project`,
	)
	if err != nil {
		return nil, fmt.Errorf("list gc policies: %w", err)
	}
	defer rows.Close()

	var policies []GCPolicy
	for rows.Next() {
		var p GCPolicy
		var keep, older sql.NullInt64
		var disabled int
		if err := rows.Scan(&p.Project, &keep, &older, &disabled); err != nil {
			return nil, err
		}
		if keep.Valid {
			v := int(keep.Int64)
			p.KeepCount = &v
		}
		if older.Valid {
			v := int(older.Int64)
			p.OlderThanDays = &v
		}
		p.Disabled = disabled != 0
		policies = append(policies, p)
	}
	return policies, rows.Err()
}

// ResolveGCPolicy returns the effective policy for a project. Returns (nil, false)
// when GC is disabled (either explicitly for the project, or globally with no
// per-project override). The built-in default keep=3 applies when neither the
// project nor the global override exists.
func (s *Store) ResolveGCPolicy(project string) (*GCPolicy, bool) {
	if p, _ := s.GetGCPolicy(project); p != nil {
		if p.Disabled {
			return nil, false
		}
		return p, true
	}
	if g, _ := s.GetGCPolicy(GCPolicyGlobalKey); g != nil {
		if g.Disabled {
			return nil, false
		}
		// Project inherits global, but the resolved struct names the project.
		out := *g
		out.Project = project
		return &out, true
	}
	def := 3
	return &GCPolicy{Project: project, KeepCount: &def}, true
}
