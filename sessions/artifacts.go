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
	"fmt"
	"log"
	"os"
	"strings"

	"google.golang.org/adk/v2/artifact"
	"google.golang.org/adk/v2/artifact/gcsartifact"
)

// NewArtifactService returns the process-wide ADK artifact service.
//
// When ARTIFACT_BUCKET_NAME or LOGS_BUCKET_NAME is set (and USE_IN_MEMORY_ARTIFACT is not true),
// it initializes a Google Cloud Storage backed artifact.Service via gcsartifact.NewService.
// Otherwise (or if GCS initialization fails in local dev without ADC), it falls back to
// artifact.InMemoryService().
func NewArtifactService(ctx context.Context) (artifact.Service, error) {
	forceInMemory := strings.ToLower(strings.TrimSpace(os.Getenv("USE_IN_MEMORY_ARTIFACT")))
	if forceInMemory == "true" || forceInMemory == "1" || forceInMemory == "yes" {
		return artifact.InMemoryService(), nil
	}

	bucketName := resolveArtifactBucketName()
	if bucketName == "" {
		return artifact.InMemoryService(), nil
	}

	svc, err := gcsartifact.NewService(ctx, bucketName)
	if err != nil {
		if strings.EqualFold(strings.TrimSpace(os.Getenv("STRICT_GCS_ARTIFACT")), "true") {
			return nil, fmt.Errorf("create GCS artifact service for bucket %q: %w", bucketName, err)
		}
		log.Printf("Warning: failed to create GCS artifact service for bucket %q (%v); falling back to in-memory artifact service", bucketName, err)
		return artifact.InMemoryService(), nil
	}
	return svc, nil
}

// resolveArtifactBucketName returns the configured GCS bucket name for ADK artifacts,
// stripping any optional "gs://" prefix and path suffix.
func resolveArtifactBucketName() string {
	for _, key := range []string{"ARTIFACT_BUCKET_NAME", "LOGS_BUCKET_NAME", "GCS_ARTIFACT_BUCKET"} {
		if raw := strings.TrimSpace(os.Getenv(key)); raw != "" {
			cleaned := strings.TrimPrefix(raw, "gs://")
			if idx := strings.IndexByte(cleaned, '/'); idx > 0 {
				cleaned = cleaned[:idx]
			}
			if cleaned != "" {
				return cleaned
			}
		}
	}
	return ""
}
