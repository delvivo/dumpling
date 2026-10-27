package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/delvivo/dumpling/internal/store"
)

// TestNoDestinationSecretLeaks: S3/SFTP secrets and unlock passphrases never appear in API
// responses, operation logs/messages or app files, also when credentials are wrong or the
// destination is unreachable (SC-005).
func TestNoDestinationSecretLeaks(t *testing.T) {
	const s3secret, sftpPw, sftpKey, unlockPass = "S3-SECRET-XYZ", "Sftp-Canary-Pw-55", "KEY-CANARY-PEM-77", "Unlock-Canary-13"
	c, s := newTestServer(t)
	c.login(t)
	var bodies []string
	call := func(method, path, body string) string {
		_, b := c.do(method, path, body, true)
		bodies = append(bodies, b)
		return b
	}

	// Saved directly: the API refuses to save a destination that fails its test.
	s3 := store.Destination{Name: "s3", Type: "s3", S3: &store.S3Config{Endpoint: "127.0.0.1:1", Region: "us-east-1",
		Bucket: "b", AccessKeyID: "AK", SecretAccessKey: s3secret}}
	sftp := store.Destination{Name: "sftp", Type: "sftp", SFTP: &store.SFTPConfig{Host: "127.0.0.1", Port: 1, Username: "u",
		Auth: "password", Password: sftpPw, RemoteDir: "/upload", HostKeyFingerprint: "SHA256:x"}}
	for _, d := range []*store.Destination{&s3, &sftp} {
		if err := s.St.SaveDestination(d); err != nil {
			t.Fatal(err)
		}
	}
	conn := store.Connection{Name: "pg", Engine: "postgres", Host: "127.0.0.1", Port: 1, Username: "u"}
	s.St.SaveConnection(&conn)

	call("GET", "/api/destinations", "")
	s3body, _ := json.Marshal(s3)
	call("POST", "/api/destinations/test", string(s3body))
	call("POST", "/api/destinations/test", `{"id":"`+s3.ID+`","name":"s3","type":"s3","s3":{"endpoint":"127.0.0.1:1","region":"us-east-1","bucket":"b","access_key_id":"AK"}}`)
	call("PUT", "/api/destinations/"+sftp.ID, `{"name":"sftp","type":"sftp","sftp":{"host":"127.0.0.1","port":1,"username":"u","auth":"key","private_key":"`+sftpKey+`","remote_dir":"/upload","host_key_fingerprint":"SHA256:x"}}`)
	call("POST", "/api/destinations/"+s3.ID+"/rescan", "")
	for _, d := range []store.Destination{s3, sftp} {
		call("POST", "/api/backups", `{"connection_id":"`+conn.ID+`","databases":["shop"],"destination_id":"`+d.ID+`"}`)
	}
	b := store.Backup{ConnectionName: "pg", Engine: "postgres", Database: "shop", DestinationID: s3.ID, Path: "x.dump.age",
		SHA256: "00", Encrypted: true, Imported: true, Status: "ok", StartedAt: store.Now(), FinishedAt: store.Now()}
	s.St.InsertBackup(&b)
	call("POST", "/api/backups/"+b.ID+"/unlock", `{"passphrase":"`+unlockPass+`","apply_to_origin":true}`)
	call("GET", "/api/backups", "")
	call("POST", "/api/backups/"+b.ID+"/verify", "") // reaches the unreachable S3 endpoint: op log + message
	b2 := b
	b2.ID, b2.DestinationID, b2.Path = "", sftp.ID, "y.dump.age"
	s.St.InsertBackup(&b2)
	call("POST", "/api/backups/"+b2.ID+"/verify", "")
	call("POST", "/api/restores", `{"backup_id":"`+b.ID+`","target_connection_id":"`+conn.ID+`","target_database":"t","passphrase":"`+unlockPass+`"}`)
	s.R.Wait()

	var ops []struct{ ID, Status, Message string }
	json.Unmarshal([]byte(call("GET", "/api/operations", "")), &ops)
	if len(ops) < 2 {
		t.Fatalf("expected failing operations, got %d", len(ops))
	}
	for _, o := range ops {
		call("GET", "/api/operations/"+o.ID+"/log", "")
	}
	canaries := []string{s3secret, sftpPw, sftpKey, unlockPass}
	for i, body := range bodies {
		for _, c := range canaries {
			if strings.Contains(body, c) {
				t.Fatalf("secret %q in API response %d: %s", c, i, body)
			}
		}
	}
	filepath.Walk(s.R.DataDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		data, _ := os.ReadFile(path)
		for _, c := range canaries {
			if strings.Contains(string(data), c) {
				t.Fatalf("secret %q found in %s", c, path)
			}
		}
		return nil
	})
}
