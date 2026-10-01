package platform

import (
	"context"
	"io"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// Opt-in: the target must be an explicitly disposable, loopback S3 fixture.
func TestBackupStreamingUploadRoundTrip(t *testing.T) {
	endpoint := os.Getenv("DUPABASE_TEST_S3_URL")
	if endpoint == "" {
		t.Skip("DUPABASE_TEST_S3_URL is not set")
	}
	u, err := url.Parse(endpoint)
	if err != nil || (u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost" && u.Hostname() != "::1") {
		t.Fatal("S3 test requires a disposable loopback fixture")
	}
	t.Setenv("BACKUP_ALLOWED_ENDPOINTS", endpoint)
	const fixtureKey = "isolated-s3-fixture-encryption-key-20261001"
	access, err := EncryptPgPassword("fixture-access", fixtureKey)
	if err != nil {
		t.Fatal(err)
	}
	secret, err := EncryptPgPassword("fixture-secret", fixtureKey)
	if err != nil {
		t.Fatal(err)
	}
	settings := &backupSettingsInternal{S3Endpoint: endpoint, S3Region: "us-east-1", S3Bucket: "dupabase-maintenance", S3AccessKeyEncrypted: access, S3SecretKeyEncrypted: secret}
	s := NewBackupService(nil, "", fixtureKey)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	key := "test-stream-" + time.Now().UTC().Format("20060102T150405.000000000")
	data := strings.Repeat("streamed-backup-fixture\n", 600000) // exercises multipart streaming
	reader, writer := io.Pipe()
	go func() { _, err := io.Copy(writer, strings.NewReader(data)); _ = writer.CloseWithError(err) }()
	defer reader.Close()
	size, err := s.uploadToS3(ctx, settings, key, reader)
	if err != nil {
		t.Fatal(err)
	}
	if size != int64(len(data)) {
		t.Fatalf("upload size = %d, want %d", size, len(data))
	}
	client, err := s.getS3Client(ctx, settings)
	if err != nil {
		t.Fatal(err)
	}
	defer client.DeleteObject(context.Background(), &s3.DeleteObjectInput{Bucket: aws.String(settings.S3Bucket), Key: aws.String(key)})
	object, err := client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(settings.S3Bucket), Key: aws.String(key)})
	if err != nil {
		t.Fatal(err)
	}
	defer object.Body.Close()
	actual, err := io.ReadAll(io.LimitReader(object.Body, int64(len(data))+1))
	if err != nil || string(actual) != data {
		t.Fatalf("streamed data changed: %v", err)
	}
}
