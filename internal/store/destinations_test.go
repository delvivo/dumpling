package store

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func s3Dest() Destination {
	return Destination{Name: "off", Type: "s3", S3: &S3Config{Endpoint: "http://minio:9000/", Region: "us-east-1",
		Bucket: "b", Prefix: "/p/", AccessKeyID: "AK", SecretAccessKey: "S3-SECRET-XYZ"}}
}

func TestDestinationSecrets(t *testing.T) {
	st := openTest(t, filepath.Join(t.TempDir(), "t.db"))
	d := s3Dest()
	if err := st.SaveDestination(&d); err != nil {
		t.Fatal(err)
	}
	if d.S3.Endpoint != "minio:9000" || d.S3.UseTLS || d.S3.Prefix != "p" {
		t.Fatalf("not normalized: %+v", d.S3)
	}
	var cfg, sealed string
	st.DB.QueryRow(`SELECT config,secret FROM destinations WHERE id=?`, d.ID).Scan(&cfg, &sealed)
	if strings.Contains(cfg+sealed, "S3-SECRET-XYZ") {
		t.Fatal("secret stored in clear")
	}
	got, _ := st.Destination(d.ID)
	if got.S3.SecretAccessKey != "S3-SECRET-XYZ" || !got.HasSecret["secret_access_key"] {
		t.Fatalf("secret not restored: %+v", got.S3)
	}
	b, _ := json.Marshal(got.Public())
	if strings.Contains(string(b), "S3-SECRET-XYZ") {
		t.Fatal("secret in public JSON")
	}

	// blank secret on update keeps the stored one
	upd := got.Public()
	upd.Name = "renamed"
	if err := st.SaveDestination(&upd); err != nil {
		t.Fatal(err)
	}
	got, _ = st.Destination(d.ID)
	if got.S3.SecretAccessKey != "S3-SECRET-XYZ" || got.Name != "renamed" {
		t.Fatalf("update lost secret: %+v", got)
	}
}

func TestDestinationLocationLock(t *testing.T) {
	st := openTest(t, filepath.Join(t.TempDir(), "t.db"))
	d := s3Dest()
	if err := st.SaveDestination(&d); err != nil {
		t.Fatal(err)
	}
	b := Backup{ConnectionName: "c", Engine: "postgres", Database: "db", DestinationID: d.ID, Path: "x.dump",
		SHA256: "00", Status: "ok", StartedAt: Now(), FinishedAt: Now()}
	if err := st.InsertBackup(&b); err != nil {
		t.Fatal(err)
	}
	moved := d.Public()
	moved.S3.Bucket = "other"
	if err := st.SaveDestination(&moved); !errors.Is(err, ErrLocationLocked) {
		t.Fatalf("bucket change: %v", err)
	}
	cred := d.Public()
	cred.S3.AccessKeyID, cred.S3.SecretAccessKey = "AK2", "NEW"
	if err := st.SaveDestination(&cred); err != nil {
		t.Fatalf("credential change: %v", err)
	}
	if err := st.DeleteDestination(d.ID); !errors.Is(err, ErrInUse) {
		t.Fatalf("delete: %v", err)
	}
}

func TestBackupLockAndUnlock(t *testing.T) {
	st := openTest(t, filepath.Join(t.TempDir(), "t.db"))
	d := Destination{Name: "disk", Path: filepath.Join(t.TempDir(), "b")}
	if err := st.SaveDestination(&d); err != nil {
		t.Fatal(err)
	}
	b := Backup{ConnectionName: "c", Engine: "mysql", Database: "db", DestinationID: d.ID, Path: "x.sql.gz.age",
		SHA256: "00", Encrypted: true, Imported: true, OriginPlanID: "P1", Status: "ok", StartedAt: Now(), FinishedAt: Now()}
	st.InsertBackup(&b)
	got, _ := st.BackupByPath(d.ID, "x.sql.gz.age")
	if !got.Locked || got.OriginPlanID != "P1" {
		t.Fatalf("want locked: %+v", got)
	}
	st.SetBackupPassphrase(b.ID, "pw")
	got, _ = st.Backup(b.ID)
	if got.Locked || got.PassphrasePlain() != "pw" {
		t.Fatalf("want unlocked: %+v", got)
	}
}
