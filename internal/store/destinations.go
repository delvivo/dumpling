package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path"
	"path/filepath"
	"strings"
)

// S3Config holds S3-compatible settings. SecretAccessKey is input/runtime only: stored sealed in
// destinations.secret and removed by Public() before responses.
type S3Config struct {
	Endpoint        string `json:"endpoint"` // host[:port]; "" = AWS
	UseTLS          bool   `json:"use_tls"`
	Region          string `json:"region"`
	Bucket          string `json:"bucket"`
	Prefix          string `json:"prefix"`
	AccessKeyID     string `json:"access_key_id"`
	PathStyle       bool   `json:"path_style"`
	SecretAccessKey string `json:"secret_access_key,omitempty"`
}

// SFTPConfig holds SFTP settings. Password, PrivateKey and KeyPassphrase are input/runtime only.
type SFTPConfig struct {
	Host               string `json:"host"`
	Port               int    `json:"port"`
	Username           string `json:"username"`
	Auth               string `json:"auth"` // password | key
	RemoteDir          string `json:"remote_dir"`
	HostKeyFingerprint string `json:"host_key_fingerprint"`
	Password           string `json:"password,omitempty"`
	PrivateKey         string `json:"private_key,omitempty"`
	KeyPassphrase      string `json:"key_passphrase,omitempty"`
}

type Destination struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	Type        string          `json:"type"` // local | s3 | sftp
	Path        string          `json:"path"`
	S3          *S3Config       `json:"s3"`
	SFTP        *SFTPConfig     `json:"sftp"`
	HasSecret   map[string]bool `json:"has_secret"`
	FreeBytes   *uint64         `json:"free_bytes,omitempty"`
	BackupCount int             `json:"backup_count"`
}

type destSecret struct {
	SecretAccessKey string `json:"secret_access_key,omitempty"`
	Password        string `json:"password,omitempty"`
	PrivateKey      string `json:"private_key,omitempty"`
	KeyPassphrase   string `json:"key_passphrase,omitempty"`
}

func (d Destination) secrets() destSecret {
	var s destSecret
	if d.S3 != nil {
		s.SecretAccessKey = d.S3.SecretAccessKey
	}
	if d.SFTP != nil {
		s.Password, s.PrivateKey, s.KeyPassphrase = d.SFTP.Password, d.SFTP.PrivateKey, d.SFTP.KeyPassphrase
	}
	return s
}

// Secrets returns the plaintext secret values, for log redaction.
func (d Destination) Secrets() []string {
	s := d.secrets()
	return []string{s.SecretAccessKey, s.Password, s.PrivateKey, s.KeyPassphrase}
}

// Public returns a copy safe for API responses: secret values removed.
func (d Destination) Public() Destination {
	if d.S3 != nil {
		c := *d.S3
		c.SecretAccessKey = ""
		d.S3 = &c
	}
	if d.SFTP != nil {
		c := *d.SFTP
		c.Password, c.PrivateKey, c.KeyPassphrase = "", "", ""
		d.SFTP = &c
	}
	return d
}

// mergeSecrets fills blank secret fields from old (blank on update = keep stored value).
func (d *Destination) mergeSecrets(old Destination) {
	o := old.secrets()
	if d.S3 != nil && d.S3.SecretAccessKey == "" {
		d.S3.SecretAccessKey = o.SecretAccessKey
	}
	if d.SFTP != nil {
		if d.SFTP.Password == "" && d.SFTP.Auth == "password" {
			d.SFTP.Password = o.Password
		}
		if d.SFTP.PrivateKey == "" && d.SFTP.Auth == "key" {
			d.SFTP.PrivateKey = o.PrivateKey
			if d.SFTP.KeyPassphrase == "" {
				d.SFTP.KeyPassphrase = o.KeyPassphrase
			}
		}
	}
}

// Normalize fills defaults: type local, S3 endpoint URL → host + use_tls, SFTP port 22 and auth,
// and drops settings that do not belong to the type.
func (d *Destination) Normalize() {
	if d.Type == "" {
		d.Type = "local"
	}
	if d.S3 != nil {
		e := strings.TrimSuffix(strings.TrimSpace(d.S3.Endpoint), "/")
		if u, err := url.Parse(e); err == nil && (u.Scheme == "http" || u.Scheme == "https") {
			e, d.S3.UseTLS = u.Host, u.Scheme == "https"
		}
		d.S3.Endpoint = e
		d.S3.Prefix = strings.Trim(d.S3.Prefix, "/")
	}
	if d.SFTP != nil {
		if d.SFTP.Port == 0 {
			d.SFTP.Port = 22
		}
		if d.SFTP.Auth == "" {
			d.SFTP.Auth = "password"
		}
		if d.SFTP.RemoteDir != "/" {
			d.SFTP.RemoteDir = strings.TrimSuffix(d.SFTP.RemoteDir, "/")
		}
		if d.SFTP.Auth == "password" {
			d.SFTP.PrivateKey, d.SFTP.KeyPassphrase = "", ""
		} else {
			d.SFTP.Password = ""
		}
	}
	switch d.Type {
	case "local":
		d.S3, d.SFTP = nil, nil
	case "s3":
		d.Path, d.SFTP = "", nil
	case "sftp":
		d.Path, d.S3 = "", nil
	}
}

// ValidationError is a user-facing invalid-input error.
type ValidationError string

func (e ValidationError) Error() string { return string(e) }

// Validate checks required fields per type (after Normalize and secret merge).
func (d Destination) Validate() error {
	if n := len(d.Name); n < 1 || n > 64 {
		return ValidationError("name must be 1-64 characters")
	}
	switch d.Type {
	case "local":
		if !filepath.IsAbs(d.Path) {
			return ValidationError("path must be absolute")
		}
	case "s3":
		c := d.S3
		if c == nil || c.Bucket == "" || c.Region == "" || c.AccessKeyID == "" || c.SecretAccessKey == "" {
			return ValidationError("s3 requires bucket, region, access key id and secret access key")
		}
	case "sftp":
		c := d.SFTP
		if c == nil || c.Host == "" || c.Username == "" || !path.IsAbs(c.RemoteDir) {
			return ValidationError("sftp requires host, username and an absolute remote directory")
		}
		if c.Port < 1 || c.Port > 65535 {
			return ValidationError("invalid port")
		}
		if (c.Auth != "password" && c.Auth != "key") || (c.Auth == "password" && c.Password == "") || (c.Auth == "key" && c.PrivateKey == "") {
			return ValidationError("sftp requires a password or a private key")
		}
		if c.HostKeyFingerprint == "" {
			return ValidationError("sftp host key fingerprint must be confirmed")
		}
	default:
		return ValidationError("unknown destination type")
	}
	return nil
}

// location identifies where backups live; it may not change while backups exist (FR-013).
func (d Destination) location() string {
	switch d.Type {
	case "s3":
		return fmt.Sprintf("s3|%s|%s|%s", d.S3.Endpoint, d.S3.Bucket, d.S3.Prefix)
	case "sftp":
		return fmt.Sprintf("sftp|%s|%d|%s", d.SFTP.Host, d.SFTP.Port, d.SFTP.RemoteDir)
	}
	return "local|" + d.Path
}

const destCols = `id,name,type,path,config,secret,
	(SELECT count(*) FROM backups b WHERE b.destination_id=destinations.id AND b.status<>'deleted')`

func (s *Store) scanDest(row interface{ Scan(...any) error }) (Destination, error) {
	var d Destination
	var cfg, sealed string
	if err := row.Scan(&d.ID, &d.Name, &d.Type, &d.Path, &cfg, &sealed, &d.BackupCount); err != nil {
		return d, wrap(err)
	}
	plain, err := s.Box.Open(sealed)
	if err != nil {
		return d, err
	}
	var sec destSecret
	if plain != "" {
		json.Unmarshal([]byte(plain), &sec)
	}
	switch d.Type {
	case "s3":
		d.S3 = &S3Config{}
		json.Unmarshal([]byte(cfg), d.S3)
		d.S3.SecretAccessKey = sec.SecretAccessKey
	case "sftp":
		d.SFTP = &SFTPConfig{}
		json.Unmarshal([]byte(cfg), d.SFTP)
		d.SFTP.Password, d.SFTP.PrivateKey, d.SFTP.KeyPassphrase = sec.Password, sec.PrivateKey, sec.KeyPassphrase
	}
	d.HasSecret = map[string]bool{}
	for k, v := range map[string]string{"secret_access_key": sec.SecretAccessKey, "password": sec.Password,
		"private_key": sec.PrivateKey, "key_passphrase": sec.KeyPassphrase} {
		if v != "" {
			d.HasSecret[k] = true
		}
	}
	return d, nil
}

func (s *Store) Destinations() ([]Destination, error) {
	rows, err := s.DB.Query(`SELECT ` + destCols + ` FROM destinations ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Destination{}
	for rows.Next() {
		d, err := s.scanDest(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *Store) Destination(id string) (Destination, error) {
	return s.scanDest(s.DB.QueryRow(`SELECT `+destCols+` FROM destinations WHERE id=?`, id))
}

// ErrLocationLocked: type or location fields cannot change while the destination holds backups.
var ErrLocationLocked = errors.New("location locked")

// PrepareDestination normalizes d, merges blank secrets from the stored row on update, enforces the
// location lock and validates. Used before testing a destination and by SaveDestination.
func (s *Store) PrepareDestination(d *Destination) error {
	d.Normalize()
	if d.ID != "" {
		old, err := s.Destination(d.ID)
		if err != nil {
			return err
		}
		d.mergeSecrets(old)
		if old.BackupCount > 0 && old.location() != d.location() {
			return ErrLocationLocked
		}
	}
	return d.Validate()
}

// SaveDestination inserts (empty ID) or updates a destination.
func (s *Store) SaveDestination(d *Destination) error {
	if err := s.PrepareDestination(d); err != nil {
		return err
	}
	pub := d.Public()
	cfg := []byte("{}")
	switch d.Type {
	case "s3":
		cfg, _ = json.Marshal(pub.S3)
	case "sftp":
		cfg, _ = json.Marshal(pub.SFTP)
	}
	plain := ""
	if sec := d.secrets(); sec != (destSecret{}) {
		b, _ := json.Marshal(sec)
		plain = string(b)
	}
	sealed, err := s.Box.Seal(plain)
	if err != nil {
		return err
	}
	if d.ID == "" {
		d.ID = NewID()
		_, err = s.DB.Exec(`INSERT INTO destinations(id,name,type,path,config,secret) VALUES(?,?,?,?,?,?)`,
			d.ID, d.Name, d.Type, d.Path, string(cfg), sealed)
	} else {
		var res sql.Result
		if res, err = s.DB.Exec(`UPDATE destinations SET name=?,type=?,path=?,config=?,secret=? WHERE id=?`,
			d.Name, d.Type, d.Path, string(cfg), sealed, d.ID); err == nil {
			err = mustAffect(res)
		}
	}
	return wrap(err)
}

// DestinationUsage lists the plans using a destination and its non-deleted backup count.
func (s *Store) DestinationUsage(id string) ([]string, int, error) {
	plans := []string{}
	rows, err := s.DB.Query(`SELECT name FROM plans WHERE destination_id=? ORDER BY name`, id)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, 0, err
		}
		plans = append(plans, n)
	}
	var backups int
	err = s.DB.QueryRow(`SELECT count(*) FROM backups WHERE destination_id=? AND status<>'deleted'`, id).Scan(&backups)
	return plans, backups, err
}

func (s *Store) DeleteDestination(id string) error {
	plans, backups, err := s.DestinationUsage(id)
	if err != nil {
		return err
	}
	if len(plans)+backups > 0 {
		return ErrInUse
	}
	s.DB.Exec(`DELETE FROM backups WHERE destination_id=? AND status='deleted'`, id)
	res, err := s.DB.Exec(`DELETE FROM destinations WHERE id=?`, id)
	if err != nil {
		return err
	}
	return mustAffect(res)
}
