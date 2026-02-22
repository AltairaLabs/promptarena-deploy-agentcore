package agentcore

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestBuildCodeDeployZIP_ValidArchive(t *testing.T) {
	// Create a temporary binary file.
	tmpDir := t.TempDir()
	binaryPath := filepath.Join(tmpDir, "promptkit-runtime")
	binaryContent := []byte("fake-binary-content-for-testing")
	if err := os.WriteFile(binaryPath, binaryContent, 0o755); err != nil {
		t.Fatalf("write temp binary: %v", err)
	}

	packJSON := `{"id":"testpack","version":"v1.0.0"}`

	zipData, err := buildCodeDeployZIP(binaryPath, packJSON)
	if err != nil {
		t.Fatalf("buildCodeDeployZIP: %v", err)
	}

	// Verify the ZIP is valid and contains expected files.
	reader, err := zip.NewReader(bytes.NewReader(zipData), int64(len(zipData)))
	if err != nil {
		t.Fatalf("open ZIP: %v", err)
	}

	expectedFiles := map[string]bool{
		"main.py":           false,
		"promptkit-runtime": false,
		"pack.json":         false,
	}

	for _, f := range reader.File {
		if _, ok := expectedFiles[f.Name]; ok {
			expectedFiles[f.Name] = true
		} else {
			t.Errorf("unexpected file in ZIP: %s", f.Name)
		}
	}

	for name, found := range expectedFiles {
		if !found {
			t.Errorf("expected file %s not found in ZIP", name)
		}
	}
}

func TestBuildCodeDeployZIP_ExecutablePermissions(t *testing.T) {
	tmpDir := t.TempDir()
	binaryPath := filepath.Join(tmpDir, "promptkit-runtime")
	if err := os.WriteFile(binaryPath, []byte("binary"), 0o755); err != nil {
		t.Fatalf("write temp binary: %v", err)
	}

	zipData, err := buildCodeDeployZIP(binaryPath, `{}`)
	if err != nil {
		t.Fatalf("buildCodeDeployZIP: %v", err)
	}

	reader, err := zip.NewReader(bytes.NewReader(zipData), int64(len(zipData)))
	if err != nil {
		t.Fatalf("open ZIP: %v", err)
	}

	for _, f := range reader.File {
		mode := f.Mode()
		if mode&0o100 == 0 {
			t.Errorf("file %s is not executable (mode: %o)", f.Name, mode)
		}
	}
}

func TestBuildCodeDeployZIP_MainPyContent(t *testing.T) {
	tmpDir := t.TempDir()
	binaryPath := filepath.Join(tmpDir, "promptkit-runtime")
	if err := os.WriteFile(binaryPath, []byte("binary"), 0o755); err != nil {
		t.Fatalf("write temp binary: %v", err)
	}

	zipData, err := buildCodeDeployZIP(binaryPath, `{}`)
	if err != nil {
		t.Fatalf("buildCodeDeployZIP: %v", err)
	}

	reader, err := zip.NewReader(bytes.NewReader(zipData), int64(len(zipData)))
	if err != nil {
		t.Fatalf("open ZIP: %v", err)
	}

	for _, f := range reader.File {
		if f.Name != "main.py" {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("open main.py: %v", err)
		}
		var buf bytes.Buffer
		if _, err := buf.ReadFrom(rc); err != nil {
			t.Fatalf("read main.py: %v", err)
		}
		rc.Close()

		content := buf.String()
		if content != mainPyContent {
			t.Errorf("main.py content mismatch:\ngot:  %q\nwant: %q", content, mainPyContent)
		}
		return
	}
	t.Fatal("main.py not found in ZIP")
}

func TestBuildCodeDeployZIP_PackJSONContent(t *testing.T) {
	tmpDir := t.TempDir()
	binaryPath := filepath.Join(tmpDir, "promptkit-runtime")
	if err := os.WriteFile(binaryPath, []byte("binary"), 0o755); err != nil {
		t.Fatalf("write temp binary: %v", err)
	}

	packJSON := `{"id":"mypack","version":"v2.0.0","name":"My Pack"}`
	zipData, err := buildCodeDeployZIP(binaryPath, packJSON)
	if err != nil {
		t.Fatalf("buildCodeDeployZIP: %v", err)
	}

	reader, err := zip.NewReader(bytes.NewReader(zipData), int64(len(zipData)))
	if err != nil {
		t.Fatalf("open ZIP: %v", err)
	}

	for _, f := range reader.File {
		if f.Name != "pack.json" {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("open pack.json: %v", err)
		}
		var buf bytes.Buffer
		if _, err := buf.ReadFrom(rc); err != nil {
			t.Fatalf("read pack.json: %v", err)
		}
		rc.Close()

		if buf.String() != packJSON {
			t.Errorf("pack.json content = %q, want %q", buf.String(), packJSON)
		}
		return
	}
	t.Fatal("pack.json not found in ZIP")
}

func TestBuildCodeDeployZIP_MissingBinary(t *testing.T) {
	_, err := buildCodeDeployZIP("/nonexistent/path/to/binary", `{}`)
	if err == nil {
		t.Fatal("expected error for missing binary")
	}
}

func TestCodeDeployS3Key(t *testing.T) {
	key := codeDeployS3Key("mypack", "v1.0.0")
	want := "promptkit/mypack/v1.0.0/deployment_package.zip"
	if key != want {
		t.Errorf("codeDeployS3Key = %q, want %q", key, want)
	}
}

func TestCodeDeployS3Bucket(t *testing.T) {
	bucket := codeDeployS3Bucket("123456789012", "us-west-2")
	want := "bedrock-agentcore-code-123456789012-us-west-2"
	if bucket != want {
		t.Errorf("codeDeployS3Bucket = %q, want %q", bucket, want)
	}
}
