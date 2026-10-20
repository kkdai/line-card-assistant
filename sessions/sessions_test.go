// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     https://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package sessions

import (
	"context"
	"testing"
)

func TestResolveArtifactBucketName(t *testing.T) {
	t.Setenv("ARTIFACT_BUCKET_NAME", "gs://my-agent-artifacts-bucket/cards/v1")
	t.Setenv("LOGS_BUCKET_NAME", "")
	if got := resolveArtifactBucketName(); got != "my-agent-artifacts-bucket" {
		t.Errorf("resolveArtifactBucketName() = %q, want %q", got, "my-agent-artifacts-bucket")
	}

	t.Setenv("ARTIFACT_BUCKET_NAME", "")
	t.Setenv("LOGS_BUCKET_NAME", "gs://my-agent-logs-bucket")
	if got := resolveArtifactBucketName(); got != "my-agent-logs-bucket" {
		t.Errorf("resolveArtifactBucketName() = %q, want %q", got, "my-agent-logs-bucket")
	}
}

func TestNewArtifactServiceDefaultInMemory(t *testing.T) {
	t.Setenv("ARTIFACT_BUCKET_NAME", "")
	t.Setenv("LOGS_BUCKET_NAME", "")
	t.Setenv("GCS_ARTIFACT_BUCKET", "")

	svc, err := NewArtifactService(context.Background())
	if err != nil {
		t.Fatalf("NewArtifactService() error = %v", err)
	}
	if svc == nil {
		t.Fatal("expected non-nil artifact.Service")
	}
}

func TestNewServiceDefaultInMemory(t *testing.T) {
	t.Setenv("SESSION_TYPE", "")
	t.Setenv("GOOGLE_CLOUD_AGENT_ENGINE_ID", "")

	svc, err := NewService(context.Background())
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	if svc == nil {
		t.Fatal("expected non-nil session.Service")
	}
}
