package agentcore

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// codeDeployEntryPoint is the Python entrypoint filename required by
// AgentCore CodeConfiguration.
const codeDeployEntryPoint = "main.py"

// codeDeployBinaryName is the name of the Go runtime binary inside the ZIP.
const codeDeployBinaryName = "promptkit-runtime"

// codeDeployPackFile is the pack JSON filename inside the ZIP.
const codeDeployPackFile = "pack.json"

// execPermission is the Unix permission mode for executable files in the ZIP.
const execPermission = 0o755

// mainPyContent is the Python entrypoint that launches the Go runtime binary.
// Note: /var/task is read-only on AgentCore, so we cannot chmod. The ZIP
// archive sets executable permissions via file headers instead.
const mainPyContent = `#!/usr/bin/env python3
"""Thin wrapper to launch the PromptKit Go runtime on AgentCore CodeConfiguration."""
import os
import subprocess
import sys

def main():
    binary = os.path.join(os.path.dirname(__file__), "promptkit-runtime")
    os.environ.setdefault("PROMPTPACK_FILE", os.path.join(os.path.dirname(__file__), "pack.json"))
    sys.exit(subprocess.call([binary]))

if __name__ == "__main__":
    main()
`

// buildCodeDeployZIP creates an in-memory ZIP archive containing the Python
// entrypoint, the pre-compiled Go runtime binary, and the pack JSON. The
// binary is read from binaryPath on disk; packJSON is the raw pack content.
func buildCodeDeployZIP(binaryPath, packJSON string) ([]byte, error) {
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)

	if err := addTextFile(w, codeDeployEntryPoint, mainPyContent); err != nil {
		return nil, fmt.Errorf("add %s: %w", codeDeployEntryPoint, err)
	}

	if err := addBinaryFile(w, codeDeployBinaryName, binaryPath); err != nil {
		return nil, fmt.Errorf("add %s: %w", codeDeployBinaryName, err)
	}

	if err := addTextFile(w, codeDeployPackFile, packJSON); err != nil {
		return nil, fmt.Errorf("add %s: %w", codeDeployPackFile, err)
	}

	if err := w.Close(); err != nil {
		return nil, fmt.Errorf("close zip: %w", err)
	}

	return buf.Bytes(), nil
}

// addTextFile adds a text file to the ZIP archive.
func addTextFile(w *zip.Writer, name, content string) error {
	header := &zip.FileHeader{
		Name:   name,
		Method: zip.Deflate,
	}
	header.SetMode(execPermission)
	f, err := w.CreateHeader(header)
	if err != nil {
		return err
	}
	_, err = f.Write([]byte(content))
	return err
}

// addBinaryFile adds a file from disk to the ZIP archive with exec permissions.
func addBinaryFile(w *zip.Writer, name, srcPath string) error {
	absPath, err := filepath.Abs(srcPath)
	if err != nil {
		return fmt.Errorf("resolve path: %w", err)
	}

	f, err := os.Open(absPath) //nolint:gosec // path is from trusted config
	if err != nil {
		return fmt.Errorf("open binary: %w", err)
	}
	defer func() { _ = f.Close() }()

	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("stat binary: %w", err)
	}

	header := &zip.FileHeader{
		Name:               name,
		Method:             zip.Deflate,
		UncompressedSize64: uint64(info.Size()), //nolint:gosec // file size is always non-negative
	}
	header.SetMode(execPermission)

	zf, err := w.CreateHeader(header)
	if err != nil {
		return err
	}

	_, err = io.Copy(zf, f)
	return err
}

// codeDeployS3Prefix returns the S3 object key prefix for a code deploy
// package: "promptkit/{packID}/{version}/".
func codeDeployS3Prefix(packID, version string) string {
	return fmt.Sprintf("promptkit/%s/%s/", packID, version)
}

// codeDeployS3Key returns the full S3 object key for the ZIP package.
func codeDeployS3Key(packID, version string) string {
	return codeDeployS3Prefix(packID, version) + "deployment_package.zip"
}

// codeDeployS3Bucket returns the conventional S3 bucket name for AgentCore
// code deploy packages: "bedrock-agentcore-code-{account}-{region}".
func codeDeployS3Bucket(accountID, region string) string {
	return fmt.Sprintf("bedrock-agentcore-code-%s-%s", accountID, region)
}

// uploadCodePackage builds the code deploy ZIP and uploads it to S3.
// Called during prepareApply when deploy_mode is "code".
func uploadCodePackage(
	ctx context.Context, client awsClient, cfg *Config, packJSON string,
) error {
	zipData, err := buildCodeDeployZIP(cfg.RuntimeBinaryPath, packJSON)
	if err != nil {
		return fmt.Errorf("build code deploy ZIP: %w", err)
	}

	accountID := extractAccountFromARN(cfg.RuntimeRoleARN)
	bucket := codeDeployS3Bucket(accountID, cfg.Region)
	packID := cfg.ResourceTags[TagKeyPackID]
	version := cfg.ResourceTags[TagKeyVersion]
	key := codeDeployS3Key(packID, version)

	if err := client.UploadCodePackage(ctx, zipData, bucket, key); err != nil {
		return fmt.Errorf("upload code package: %w", err)
	}

	return nil
}
