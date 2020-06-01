/*
Copyright 2018 The Kubernetes Authors.

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

package main

import (
	"github.com/spf13/pflag"
	"os"

	"github.com/argoproj/argo-cd/errors"
	argocdclient "github.com/argoproj/argo-cd/pkg/apiclient"
	"github.com/argoproj/argo-cd/util/config"
	"github.com/argoproj/argo-cd/util/localconfig"
	"github.com/keikoproj/cd-guard/pkg/cmd"
)

var logLevel string

func main() {
	flags := pflag.NewFlagSet("cd-guard", pflag.ExitOnError)
	pflag.CommandLine = flags
	flags.ParseErrorsWhitelist.UnknownFlags = true

	var (
		clientOpts argocdclient.ClientOptions
	)

	command := cmd.NewCmdGuard(&clientOpts)

	defaultLocalConfigPath, err := localconfig.DefaultLocalConfigPath()
	errors.CheckError(err)
	command.PersistentFlags().StringVar(&clientOpts.ConfigPath, "config", config.GetFlag("config", defaultLocalConfigPath), "Path to Argo CD config")
	command.PersistentFlags().StringVar(&clientOpts.ServerAddr, "server", config.GetFlag("server", ""), "Argo CD server address")
	command.PersistentFlags().BoolVar(&clientOpts.PlainText, "plaintext", config.GetBoolFlag("plaintext"), "Disable TLS")
	command.PersistentFlags().BoolVar(&clientOpts.Insecure, "insecure", config.GetBoolFlag("insecure"), "Skip server certificate and domain verification")
	command.PersistentFlags().StringVar(&clientOpts.CertFile, "server-crt", config.GetFlag("server-crt", ""), "Server certificate file")
	command.PersistentFlags().StringVar(&clientOpts.AuthToken, "auth-token", config.GetFlag("auth-token", ""), "Authentication token")
	command.PersistentFlags().BoolVar(&clientOpts.GRPCWeb, "grpc-web", config.GetBoolFlag("grpc-web"), "Enables gRPC-web protocol. Useful if Argo CD server is behind proxy which does not support HTTP2.")
	command.PersistentFlags().StringVar(&logLevel, "loglevel", config.GetFlag("loglevel", "info"), "Set the logging level. One of: debug|info|warn|error")

	//Ignore unknown flags

	command.Flags().ParseErrorsWhitelist.UnknownFlags = true
	command.PersistentFlags().ParseErrorsWhitelist.UnknownFlags = true

	if err := command.Execute(); err != nil {
		os.Exit(1)
	}
}
