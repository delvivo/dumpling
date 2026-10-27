package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
)

// ---- users & sessions ----

func (s *Store) UserCount() (n int, err error) {
	err = s.DB.QueryRow(`SELECT count(*) FROM users`).Scan(&n)
	return
}

func (s *Store) CreateUser(username, hash string) error {
	_, err := s.DB.Exec(`INSERT INTO users(id,username,password_hash,created_at) VALUES(?,?,?,?)`, NewID(), username, hash, Now())
	return wrap(err)
}

func (s *Store) UserByName(username string) (id, hash string, err error) {
	err = wrap(s.DB.QueryRow(`SELECT id,password_hash FROM users WHERE username=?`, username).Scan(&id, &hash))
	return
}

func (s *Store) PutSession(tokenHash, userID, expires string) error {
	_, err := s.DB.Exec(`INSERT INTO sessions(token_hash,user_id,expires_at) VALUES(?,?,?)
		ON CONFLICT(token_hash) DO UPDATE SET expires_at=excluded.expires_at`, tokenHash, userID, expires)
	return err
}

// Session returns user id and username for a valid (non-expired) session.
func (s *Store) Session(tokenHash string) (userID, username string, err error) {
	err = wrap(s.DB.QueryRow(`SELECT u.id,u.username FROM sessions s JOIN users u ON u.id=s.user_id
		WHERE s.token_hash=? AND s.expires_at>?`, tokenHash, Now()).Scan(&userID, &username))
	return
}

func (s *Store) DeleteSession(tokenHash string) error {
	_, err := s.DB.Exec(`DELETE FROM sessions WHERE token_hash=? OR expires_at<=?`, tokenHash, Now())
	return err
}

// ---- connections ----

type Connection struct {
	ID              string `json:"id"`
	Name            string `json:"name"`
	Engine          string `json:"engine"`
	Host            string `json:"host"`
	Port            int    `json:"port"`
	Username        string `json:"username"`
	Password        string `json:"password,omitempty"` // input only; cleared before responses
	PasswordSet     bool   `json:"password_set"`
	TLSMode         string `json:"tls_mode"`
	SharePathServer string `json:"share_path_server"`
	SharePathLocal  string `json:"share_path_local"`
	ServerVersion   string `json:"server_version"`
	TestedAt        string `json:"tested_at"`
}

const connCols = `id,name,engine,host,port,username,password,tls_mode,share_path_server,share_path_local,server_version,tested_at`

func (s *Store) scanConn(row interface{ Scan(...any) error }) (Connection, error) {
	var c Connection
	var sealed string
	if err := row.Scan(&c.ID, &c.Name, &c.Engine, &c.Host, &c.Port, &c.Username, &sealed, &c.TLSMode,
		&c.SharePathServer, &c.SharePathLocal, &c.ServerVersion, &c.TestedAt); err != nil {
		return c, wrap(err)
	}
	pw, err := s.Box.Open(sealed)
	c.Password, c.PasswordSet = pw, sealed != ""
	return c, err
}

func (s *Store) Connections() ([]Connection, error) {
	rows, err := s.DB.Query(`SELECT ` + connCols + ` FROM connections ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Connection{}
	for rows.Next() {
		c, err := s.scanConn(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) Connection(id string) (Connection, error) {
	return s.scanConn(s.DB.QueryRow(`SELECT `+connCols+` FROM connections WHERE id=?`, id))
}

// SaveConnection inserts (empty ID) or updates. Password is always written as given (caller merges).
func (s *Store) SaveConnection(c *Connection) error {
	sealed, err := s.Box.Seal(c.Password)
	if err != nil {
		return err
	}
	if c.ID == "" {
		c.ID = NewID()
		_, err = s.DB.Exec(`INSERT INTO connections(`+connCols+`) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
			c.ID, c.Name, c.Engine, c.Host, c.Port, c.Username, sealed, c.TLSMode, c.SharePathServer, c.SharePathLocal, c.ServerVersion, c.TestedAt)
	} else {
		var res sql.Result
		res, err = s.DB.Exec(`UPDATE connections SET name=?,engine=?,host=?,port=?,username=?,password=?,tls_mode=?,
			share_path_server=?,share_path_local=?,server_version=?,tested_at=? WHERE id=?`,
			c.Name, c.Engine, c.Host, c.Port, c.Username, sealed, c.TLSMode, c.SharePathServer, c.SharePathLocal, c.ServerVersion, c.TestedAt, c.ID)
		if err == nil {
			err = mustAffect(res)
		}
	}
	return wrap(err)
}

func (s *Store) SetConnectionVersion(id, version string) error {
	_, err := s.DB.Exec(`UPDATE connections SET server_version=?,tested_at=? WHERE id=?`, version, Now(), id)
	return err
}

// ErrInUse is returned when deleting an entity still referenced.
var ErrInUse = fmt.Errorf("in use")

func (s *Store) DeleteConnection(id string) error {
	var n int
	s.DB.QueryRow(`SELECT count(*) FROM plans WHERE connection_id=?`, id).Scan(&n)
	if n > 0 {
		return ErrInUse
	}
	res, err := s.DB.Exec(`DELETE FROM connections WHERE id=?`, id)
	if err != nil {
		return err
	}
	return mustAffect(res)
}

func mustAffect(res sql.Result) error {
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ---- plans ----

type Retention struct {
	Last    int `json:"last"`
	Daily   int `json:"daily"`
	Weekly  int `json:"weekly"`
	Monthly int `json:"monthly"`
}

type Plan struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	ConnectionID  string    `json:"connection_id"`
	Databases     []string  `json:"databases"`
	DestinationID string    `json:"destination_id"`
	Schedule      string    `json:"schedule"`
	Retention     Retention `json:"retention"`
	Passphrase    *string   `json:"passphrase,omitempty"` // input only; nil = keep, "" = remove
	Encrypted     bool      `json:"encrypted"`
	Paused        bool      `json:"paused"`
	CreatedAt     string    `json:"created_at"`
	UpdatedAt     string    `json:"updated_at"`

	passphrase string // decrypted, for runner use
}

// PassphrasePlain returns the decrypted stored passphrase ("" if not encrypted).
func (p Plan) PassphrasePlain() string { return p.passphrase }

const planCols = `id,name,connection_id,databases,destination_id,schedule,retention,passphrase,paused,created_at,updated_at`

func (s *Store) scanPlan(row interface{ Scan(...any) error }) (Plan, error) {
	var p Plan
	var dbs, ret, sealed string
	if err := row.Scan(&p.ID, &p.Name, &p.ConnectionID, &dbs, &p.DestinationID, &p.Schedule, &ret, &sealed,
		&p.Paused, &p.CreatedAt, &p.UpdatedAt); err != nil {
		return p, wrap(err)
	}
	json.Unmarshal([]byte(dbs), &p.Databases)
	json.Unmarshal([]byte(ret), &p.Retention)
	var err error
	p.passphrase, err = s.Box.Open(sealed)
	p.Encrypted = sealed != ""
	return p, err
}

func (s *Store) Plans() ([]Plan, error) {
	rows, err := s.DB.Query(`SELECT ` + planCols + ` FROM plans ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Plan{}
	for rows.Next() {
		p, err := s.scanPlan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) Plan(id string) (Plan, error) {
	return s.scanPlan(s.DB.QueryRow(`SELECT `+planCols+` FROM plans WHERE id=?`, id))
}

// SavePlan inserts or updates; p.Passphrase nil keeps the stored one on update.
func (s *Store) SavePlan(p *Plan) error {
	dbs, _ := json.Marshal(p.Databases)
	ret, _ := json.Marshal(p.Retention)
	now := Now()
	if p.ID == "" {
		pass := ""
		if p.Passphrase != nil {
			pass = *p.Passphrase
		}
		sealed, err := s.Box.Seal(pass)
		if err != nil {
			return err
		}
		p.ID, p.CreatedAt, p.UpdatedAt = NewID(), now, now
		_, err = s.DB.Exec(`INSERT INTO plans(`+planCols+`) VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
			p.ID, p.Name, p.ConnectionID, string(dbs), p.DestinationID, p.Schedule, string(ret), sealed, p.Paused, now, now)
		return wrap(err)
	}
	res, err := s.DB.Exec(`UPDATE plans SET name=?,connection_id=?,databases=?,destination_id=?,schedule=?,retention=?,paused=?,updated_at=? WHERE id=?`,
		p.Name, p.ConnectionID, string(dbs), p.DestinationID, p.Schedule, string(ret), p.Paused, now, p.ID)
	if err == nil {
		err = mustAffect(res)
	}
	if err == nil && p.Passphrase != nil {
		var sealed string
		if sealed, err = s.Box.Seal(*p.Passphrase); err == nil {
			_, err = s.DB.Exec(`UPDATE plans SET passphrase=? WHERE id=?`, sealed, p.ID)
		}
	}
	return wrap(err)
}

func (s *Store) SetPlanPaused(id string, paused bool) error {
	res, err := s.DB.Exec(`UPDATE plans SET paused=?,updated_at=? WHERE id=?`, paused, Now(), id)
	if err != nil {
		return err
	}
	return mustAffect(res)
}

func (s *Store) DeletePlan(id string) error {
	res, err := s.DB.Exec(`DELETE FROM plans WHERE id=?`, id)
	if err != nil {
		return err
	}
	return mustAffect(res)
}

// ---- backups ----

type Backup struct {
	ID             string `json:"id"`
	OperationID    string `json:"operation_id"`
	PlanID         string `json:"plan_id"`
	PlanName       string `json:"plan_name"`
	ConnectionID   string `json:"connection_id"`
	ConnectionName string `json:"connection_name"`
	Engine         string `json:"engine"`
	ServerVersion  string `json:"server_version"`
	Database       string `json:"database"`
	DestinationID  string `json:"destination_id"`
	Path           string `json:"path"`
	SizeBytes      int64  `json:"size_bytes"`
	SHA256         string `json:"sha256"`
	Encrypted      bool   `json:"encrypted"`
	Status         string `json:"status"`
	StartedAt      string `json:"started_at"`
	FinishedAt     string `json:"finished_at"`
	Imported       bool   `json:"imported"`       // created by a destination rescan
	OriginPlanID   string `json:"origin_plan_id"` // plan id recorded in the sidecar (imported backups)
	Locked         bool   `json:"locked"`         // imported, encrypted, passphrase unknown
	// Set by the API layer:
	DestinationType string `json:"destination_type"`
	Location        string `json:"location"`

	passphrase string // decrypted, set by Unlock
}

// PassphrasePlain returns the passphrase stored by Unlock ("" if none).
func (b Backup) PassphrasePlain() string { return b.passphrase }

const backupCols = `id,COALESCE(operation_id,''),COALESCE(plan_id,''),plan_name,COALESCE(connection_id,''),connection_name,engine,
	server_version,database,destination_id,path,size_bytes,sha256,encrypted,status,started_at,finished_at,imported,origin_plan_id,passphrase`

func (s *Store) scanBackup(row interface{ Scan(...any) error }) (b Backup, err error) {
	var sealed string
	if err = wrap(row.Scan(&b.ID, &b.OperationID, &b.PlanID, &b.PlanName, &b.ConnectionID, &b.ConnectionName, &b.Engine,
		&b.ServerVersion, &b.Database, &b.DestinationID, &b.Path, &b.SizeBytes, &b.SHA256, &b.Encrypted, &b.Status, &b.StartedAt,
		&b.FinishedAt, &b.Imported, &b.OriginPlanID, &sealed)); err != nil {
		return
	}
	b.passphrase, err = s.Box.Open(sealed)
	b.Locked = b.Imported && b.Encrypted && b.passphrase == ""
	return
}

func (s *Store) InsertBackup(b *Backup) error {
	if b.ID == "" {
		b.ID = NewID()
	}
	_, err := s.DB.Exec(`INSERT INTO backups(id,operation_id,plan_id,plan_name,connection_id,connection_name,engine,server_version,
		database,destination_id,path,size_bytes,sha256,encrypted,status,started_at,finished_at,imported,origin_plan_id)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		b.ID, null(b.OperationID), null(b.PlanID), b.PlanName, null(b.ConnectionID), b.ConnectionName, b.Engine, b.ServerVersion,
		b.Database, b.DestinationID, b.Path, b.SizeBytes, b.SHA256, b.Encrypted, b.Status, b.StartedAt, b.FinishedAt,
		b.Imported, b.OriginPlanID)
	b.Locked = b.Imported && b.Encrypted
	return err
}

func (s *Store) Backup(id string) (Backup, error) {
	return s.scanBackup(s.DB.QueryRow(`SELECT `+backupCols+` FROM backups WHERE id=?`, id))
}

// BackupByPath returns the non-deleted backup stored at path in a destination.
func (s *Store) BackupByPath(destID, path string) (Backup, error) {
	return s.scanBackup(s.DB.QueryRow(`SELECT `+backupCols+` FROM backups WHERE destination_id=? AND path=? AND status<>'deleted'`, destID, path))
}

// SetBackupPassphrase stores an unlock passphrase sealed at rest.
func (s *Store) SetBackupPassphrase(id, plain string) error {
	sealed, err := s.Box.Seal(plain)
	if err != nil {
		return err
	}
	_, err = s.DB.Exec(`UPDATE backups SET passphrase=? WHERE id=?`, sealed, id)
	return err
}

// OperationExists reports whether an operation id belongs to this install.
func (s *Store) OperationExists(id string) bool {
	var n int
	s.DB.QueryRow(`SELECT count(*) FROM operations WHERE id=?`, id).Scan(&n)
	return n > 0
}

type BackupFilter struct{ PlanID, ConnectionID, Database, Status, DestinationID string }

func (s *Store) Backups(f BackupFilter) ([]Backup, error) {
	q, args := `SELECT `+backupCols+` FROM backups WHERE 1=1`, []any{}
	for col, v := range map[string]string{"plan_id": f.PlanID, "connection_id": f.ConnectionID, "database": f.Database,
		"status": f.Status, "destination_id": f.DestinationID} {
		if v != "" {
			q += " AND " + col + "=?"
			args = append(args, v)
		}
	}
	if f.Status == "" {
		q += " AND status<>'deleted'"
	}
	rows, err := s.DB.Query(q+" ORDER BY started_at DESC, id DESC", args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Backup{}
	for rows.Next() {
		b, err := s.scanBackup(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func (s *Store) SetBackupStatus(id, status string) error {
	_, err := s.DB.Exec(`UPDATE backups SET status=? WHERE id=?`, status, id)
	return err
}

// ---- operations ----

type Operation struct {
	ID                 string `json:"id"`
	Type               string `json:"type"`
	Status             string `json:"status"`
	PlanID             string `json:"plan_id"`
	ConnectionID       string `json:"connection_id"`
	BackupID           string `json:"backup_id"`
	TargetConnectionID string `json:"target_connection_id"`
	TargetDatabase     string `json:"target_database"`
	Database           string `json:"database"`
	Message            string `json:"message"`
	CreatedAt          string `json:"created_at"`
	StartedAt          string `json:"started_at"`
	FinishedAt         string `json:"finished_at"`
}

const opCols = `id,type,status,COALESCE(plan_id,''),COALESCE(connection_id,''),COALESCE(backup_id,''),COALESCE(target_connection_id,''),
	target_database,database,message,created_at,started_at,finished_at`

func scanOp(row interface{ Scan(...any) error }) (o Operation, err error) {
	err = wrap(row.Scan(&o.ID, &o.Type, &o.Status, &o.PlanID, &o.ConnectionID, &o.BackupID, &o.TargetConnectionID,
		&o.TargetDatabase, &o.Database, &o.Message, &o.CreatedAt, &o.StartedAt, &o.FinishedAt))
	return
}

func (s *Store) InsertOperation(o *Operation) error {
	if o.ID == "" {
		o.ID = NewID()
	}
	if o.CreatedAt == "" {
		o.CreatedAt = Now()
	}
	_, err := s.DB.Exec(`INSERT INTO operations(id,type,status,plan_id,connection_id,backup_id,target_connection_id,target_database,
		database,message,created_at,started_at,finished_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		o.ID, o.Type, o.Status, null(o.PlanID), null(o.ConnectionID), null(o.BackupID), null(o.TargetConnectionID), o.TargetDatabase,
		o.Database, o.Message, o.CreatedAt, o.StartedAt, o.FinishedAt)
	return err
}

// UpdateOperation writes the mutable fields (status, message, backup_id, timestamps).
func (s *Store) UpdateOperation(o *Operation) error {
	_, err := s.DB.Exec(`UPDATE operations SET status=?,message=?,backup_id=?,started_at=?,finished_at=? WHERE id=?`,
		o.Status, o.Message, null(o.BackupID), o.StartedAt, o.FinishedAt, o.ID)
	return err
}

func (s *Store) Operation(id string) (Operation, error) {
	return scanOp(s.DB.QueryRow(`SELECT `+opCols+` FROM operations WHERE id=?`, id))
}

type OpFilter struct {
	Type, Status, PlanID, ConnectionID, Before string
	Limit                                      int
}

func (s *Store) Operations(f OpFilter) ([]Operation, error) {
	q, args := `SELECT `+opCols+` FROM operations WHERE 1=1`, []any{}
	add := func(cond, v string) {
		if v != "" {
			q += " AND " + cond
			args = append(args, v)
		}
	}
	add("type=?", f.Type)
	add("status=?", f.Status)
	add("plan_id=?", f.PlanID)
	if f.ConnectionID != "" {
		q += " AND (connection_id=? OR target_connection_id=?)"
		args = append(args, f.ConnectionID, f.ConnectionID)
	}
	add("id<?", f.Before)
	if f.Limit <= 0 || f.Limit > 200 {
		f.Limit = 50
	}
	rows, err := s.DB.Query(q+fmt.Sprintf(" ORDER BY id DESC LIMIT %d", f.Limit), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Operation{}
	for rows.Next() {
		o, err := scanOp(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// LastOperation returns the newest operation for a plan (zero value if none).
func (s *Store) LastOperation(planID string) Operation {
	o, _ := scanOp(s.DB.QueryRow(`SELECT `+opCols+` FROM operations WHERE plan_id=? AND type='backup' ORDER BY id DESC LIMIT 1`, planID))
	return o
}

// Unfinished returns operations left pending/running (used by startup recovery).
func (s *Store) Unfinished() ([]Operation, error) {
	rows, err := s.DB.Query(`SELECT ` + opCols + ` FROM operations WHERE status IN ('pending','running')`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Operation
	for rows.Next() {
		o, err := scanOp(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}
