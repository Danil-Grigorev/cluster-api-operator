/*
Copyright 2025 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"

	v1 "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/spf13/cobra"
	oras "oras.land/oras-go/v2"
	"oras.land/oras-go/v2/content/file"
	"oras.land/oras-go/v2/registry/remote"
	"oras.land/oras-go/v2/registry/remote/auth"
	"oras.land/oras-go/v2/registry/remote/retry"
)

type publishManifestsOptions struct {
	ociUrl string
	dir    string
	files  []string
}

var publishOpts = &publishManifestsOptions{}

var publishCmd = &cobra.Command{
	Use:     "publish",
	GroupID: groupManagement,
	Short:   "publish provider manifests to OCI registry",
	Long: LongDesc(`
		Publishes provider manifests to an OCI registry.
	`),
	Example: Examples(`
		# Publish provider manifests to the OCI destination
		capioperator publish -u ttl.sh/${IMAGE_NAME}:5m -d manifests

		# Publish manifests from files to the OCI destination
		capioperator publish -u ttl.sh/${IMAGE_NAME}:5m -f metadata.yaml -f infrastructure-components.yaml
	`),
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runPublish()
	},
}

func init() {
	publishCmd.PersistentFlags().StringVarP(&publishOpts.dir, "dir", "d", ".", `Directory with provider manifests`)
	publishCmd.PersistentFlags().StringSliceVarP(&publishOpts.files, "file", "f", []string{}, `Provider manifes file`)
	publishCmd.Flags().StringVarP(&publishOpts.ociUrl, "artifact-url", "u", "",
		"The URL of the OCI artifact to collect component manifests from.")

	RootCmd.AddCommand(publishCmd)
}

func runPublish() (err error) {
	// 0. Create a file store
	fs, err := file.New(publishOpts.dir)
	if err != nil {
		return err
	}
	defer func() {
		err = fs.Close()
	}()

	ctx := context.Background()

	// 1. Add files to the file store
	mediaType := "application/vnd.test.file"
	fileDescriptors := []v1.Descriptor{}

	files, err := os.ReadDir(publishOpts.dir)
	if err != nil {
		return err
	}

	for _, file := range files {
		if !file.Type().IsRegular() {
			continue
		}

		fileDescriptor, err := fs.Add(ctx, file.Name(), mediaType, "")
		if err != nil {
			return err
		}

		fileDescriptors = append(fileDescriptors, fileDescriptor)

		fmt.Printf("Added file: %s\n", file.Name())
	}

	for _, file := range publishOpts.files {
		fileDescriptor, err := fs.Add(ctx, file, mediaType, "")
		if err != nil {
			return err
		}

		fileDescriptors = append(fileDescriptors, fileDescriptor)

		fmt.Printf("Added custom file: %s\n", file)
	}

	// 2. Pack the files and tag the packed manifest
	artifactType := "application/vnd.acme.config"
	opts := oras.PackManifestOptions{
		Layers: fileDescriptors,
	}

	manifestDescriptor, err := oras.PackManifest(ctx, fs, oras.PackManifestVersion1_1, artifactType, opts)
	if err != nil {
		return err
	}

	fmt.Println("Packaged manifests")

	parts := strings.Split(publishOpts.ociUrl, ":")

	tag := parts[len(parts)-1]
	if err = fs.Tag(ctx, manifestDescriptor, tag); err != nil {
		return err
	}

	// 3. Connect to a remote repository
	reg := strings.Split(publishOpts.ociUrl, "/")[0]

	repo, err := remote.NewRepository(publishOpts.ociUrl)
	if err != nil {
		return err
	}

	if creds := ociAuthentication(); creds != nil {
		repo.Client = &auth.Client{
			Client:     retry.DefaultClient,
			Cache:      auth.NewCache(),
			Credential: auth.StaticCredential(reg, *creds),
		}
	}

	// 4. Copy from the file store to the remote repository
	_, err = oras.Copy(ctx, fs, tag, repo, tag, oras.DefaultCopyOptions)
	if err != nil {
		return err
	}

	return nil
}