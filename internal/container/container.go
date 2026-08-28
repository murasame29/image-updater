// Package container wires the application together.
//
// This is the one place that decides which adapters back the ports, which is why
// supporting another registry, another git host or another manifest format is a
// change to this file plus one new package.
package container

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ecr"
	awssqs "github.com/aws/aws-sdk-go-v2/service/sqs"
	"go.uber.org/dig"

	application "github.com/murasame29/image-updater/internal/application/updater"
	"github.com/murasame29/image-updater/internal/config"
	githost "github.com/murasame29/image-updater/internal/infrastructure/githost/github"
	"github.com/murasame29/image-updater/internal/infrastructure/manifest/kustomize"
	sqssource "github.com/murasame29/image-updater/internal/infrastructure/queue/sqs"
	"github.com/murasame29/image-updater/internal/infrastructure/registry"
	ecradapter "github.com/murasame29/image-updater/internal/infrastructure/registry/ecr"
	"github.com/murasame29/image-updater/internal/infrastructure/registry/oci"
	"github.com/murasame29/image-updater/internal/infrastructure/ruleset"
	"github.com/murasame29/image-updater/internal/model"
	"github.com/murasame29/image-updater/pkg/lifecycle"
)

// The worker has to be startable by the process lifecycle.
var _ lifecycle.Application = (*application.Worker)(nil)

// BuildWorker assembles the event driven updater.
//
// Returns:
//
//	The worker to run, or an error naming the dependency that could not be built.
func BuildWorker(ctx context.Context, cfg config.Config) (*application.Worker, error) {
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to load the AWS configuration: %w", err)
	}

	c := dig.New()

	providers := []any{
		func() config.Config { return cfg },
		func() aws.Config { return awsCfg },
		provideRuleSet,
		provideMetadataResolver,
		provideManifestPatcher,
		provideManifestRepository,
		provideEventDecoder,
		provideEventSource,
		provideService,
		provideEventHandler,
		provideWorker,
	}

	for _, provider := range providers {
		if err := c.Provide(provider); err != nil {
			return nil, fmt.Errorf("failed to register a provider: %w", err)
		}
	}

	var worker *application.Worker
	if err := c.Invoke(func(w *application.Worker) { worker = w }); err != nil {
		return nil, fmt.Errorf("failed to build the worker: %w", err)
	}

	return worker, nil
}

func provideRuleSet(cfg config.Config) (model.RuleSet, error) {
	return ruleset.Load(cfg.App.RulePath)
}

// provideMetadataResolver registers one resolver per supported registry.
//
// Reading the metadata is the OCI distribution API, which every registry serves,
// so a new registry only brings its own authentication: build an oci.Client with
// that authenticator and add it to the map.
func provideMetadataResolver(awsCfg aws.Config) model.MetadataResolver {
	return registry.NewRouter(map[model.RegistryKind]model.MetadataResolver{
		ecradapter.Kind: oci.NewClient(ecradapter.NewAuthenticator(ecr.NewFromConfig(awsCfg))),
	})
}

func provideManifestPatcher() model.ManifestPatcher {
	return kustomize.NewPatcher()
}

func provideManifestRepository(cfg config.Config) (model.ManifestRepository, error) {
	return githost.NewRepository(githost.Config{
		ApplicationID:  cfg.GitHub.ApplicationID,
		InstallationID: cfg.GitHub.InstallationID,
		Username:       cfg.GitHub.Username,
		PrivateKeyPath: cfg.GitHub.PrivateKeyPath,
		AuthorEmail:    cfg.GitHub.AuthorEmail,
		BaseBranch:     cfg.GitHub.BaseBranch,
		WorkDir:        cfg.App.WorkDir,
	})
}

// provideEventDecoder picks the payload schema the event source carries. Pairing
// a different decoder with the same source is how another registry starts
// feeding the updater.
func provideEventDecoder() model.EventDecoder {
	return ecradapter.Decoder{}
}

func provideEventSource(cfg config.Config, awsCfg aws.Config, decoder model.EventDecoder) (model.EventSource, error) {
	return sqssource.NewSource(awssqs.NewFromConfig(awsCfg), decoder, sqssource.Config{
		QueueURL:          cfg.AWS.QueueURL,
		MaxMessages:       cfg.AWS.MaxMessages,
		VisibilityTimeout: cfg.AWS.VisibilityTimeout,
		WaitTime:          cfg.AWS.WaitTime,
		Concurrency:       cfg.App.Concurrency,
		PollInterval:      cfg.App.PollInterval,
	})
}

func provideService(
	rules model.RuleSet,
	resolver model.MetadataResolver,
	manifests model.ManifestRepository,
	patcher model.ManifestPatcher,
) (*application.Service, error) {
	return application.NewService(rules, resolver, manifests, patcher)
}

func provideEventHandler(service *application.Service) model.EventHandler {
	return service
}

func provideWorker(source model.EventSource, handler model.EventHandler) (*application.Worker, error) {
	return application.NewWorker(source, handler)
}
