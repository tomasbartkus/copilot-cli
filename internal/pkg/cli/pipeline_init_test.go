// Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package cli

import (
	"bytes"
	"errors"
	"fmt"
	"testing"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/session"

	"github.com/aws/copilot-cli/internal/pkg/aws/secretsmanager"
	"github.com/aws/copilot-cli/internal/pkg/cli/mocks"
	"github.com/aws/copilot-cli/internal/pkg/config"
	"github.com/aws/copilot-cli/internal/pkg/deploy/cloudformation/stack"
	"github.com/aws/copilot-cli/internal/pkg/template"
	templatemocks "github.com/aws/copilot-cli/internal/pkg/template/mocks"
	"github.com/aws/copilot-cli/internal/pkg/workspace"
	"github.com/golang/mock/gomock"
	"github.com/spf13/afero"
	"github.com/stretchr/testify/require"
)

func TestInitPipelineOpts_Validate(t *testing.T) {
	testCases := map[string]struct {
		inAppName     string
		inWsAppName   string
		inrepoURL     string
		inEnvs        []string
		setupMocks    func(m *mocks.Mockstore)
		expectedError error
	}{
		"empty workspace app name": {
			inWsAppName:   "",
			setupMocks:    func(m *mocks.Mockstore) {},
			expectedError: errNoAppInWorkspace,
		},
		"invalid app name (not in workspace)": {
			inWsAppName:   "diff-app",
			inAppName:     "ghost-app",
			setupMocks:    func(m *mocks.Mockstore) {},
			expectedError: errors.New("cannot specify app ghost-app because the workspace is already registered with app diff-app"),
		},
		"invalid app name": {
			inWsAppName: "ghost-app",
			inAppName:   "ghost-app",
			setupMocks: func(m *mocks.Mockstore) {
				m.EXPECT().GetApplication("ghost-app").Return(nil, errors.New("some error"))
			},

			expectedError: fmt.Errorf("get application ghost-app configuration: some error"),
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			// GIVEN
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockStore := mocks.NewMockstore(ctrl)

			tc.setupMocks(mockStore)

			opts := &initPipelineOpts{
				initPipelineVars: initPipelineVars{
					appName:      tc.inAppName,
					repoURL:      tc.inrepoURL,
					environments: tc.inEnvs,
				},
				store:     mockStore,
				wsAppName: tc.inWsAppName,
			}

			// WHEN
			err := opts.Validate()

			// THEN
			if tc.expectedError != nil {
				require.EqualError(t, err, tc.expectedError.Error())
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestInitPipelineOpts_Ask(t *testing.T) {
	githubAnotherURL := "git@github.com:goodGoose/bhaOS.git"
	githubToken := "hunter2"
	testCases := map[string]struct {
		inEnvironments      []string
		inRepoURL           string
		inGitHubAccessToken string
		inGitBranch         string

		mockPrompt       func(m *mocks.Mockprompter)
		mockRunner       func(m *mocks.Mockrunner)
		mockSessProvider func(m *mocks.MocksessionProvider)
		mockSelector     func(m *mocks.MockpipelineEnvSelector)
		mockStore        func(m *mocks.Mockstore)
		buffer           bytes.Buffer

		expectedError error
	}{
		"passed-in URL to unsupported repo provider": {
			inRepoURL:        "unsupported.org/repositories/repoName",
			inEnvironments:   []string{"test"},
			mockStore:        func(m *mocks.Mockstore) {},
			mockSelector:     func(m *mocks.MockpipelineEnvSelector) {},
			mockRunner:       func(m *mocks.Mockrunner) {},
			mockPrompt:       func(m *mocks.Mockprompter) {},
			mockSessProvider: func(m *mocks.MocksessionProvider) {},

			expectedError: errors.New("repository unsupported.org/repositories/repoName must be from a supported provider: GitHub, CodeCommit or Bitbucket"),
		},
		"passed-in invalid environments": {
			inRepoURL:      "https://github.com/badGoose/chaOS",
			inEnvironments: []string{"test", "prod"},

			mockStore: func(m *mocks.Mockstore) {
				m.EXPECT().GetEnvironment("my-app", "test").Return(nil, errors.New("some error"))
			},
			mockSelector:     func(m *mocks.MockpipelineEnvSelector) {},
			mockRunner:       func(m *mocks.Mockrunner) {},
			mockPrompt:       func(m *mocks.Mockprompter) {},
			mockSessProvider: func(m *mocks.MocksessionProvider) {},

			expectedError: errors.New("validate environment test: some error"),
		},
		"success with GH repo with env and repoURL flags": {
			inEnvironments: []string{"test", "prod"},
			inRepoURL:      "https://github.com/badGoose/chaOS",

			mockStore: func(m *mocks.Mockstore) {
				m.EXPECT().GetEnvironment("my-app", "test").Return(
					&config.Environment{
						Name: "test",
					}, nil)
				m.EXPECT().GetEnvironment("my-app", "prod").Return(
					&config.Environment{
						Name: "prod",
					}, nil)
			},
			mockRunner:       func(m *mocks.Mockrunner) {},
			mockPrompt:       func(m *mocks.Mockprompter) {},
			mockSessProvider: func(m *mocks.MocksessionProvider) {},
			mockSelector:     func(m *mocks.MockpipelineEnvSelector) {},
		},
		"success with CC repo with env and repoURL flags": {
			inEnvironments: []string{"test", "prod"},
			inRepoURL:      "https://git-codecommit.us-west-2.amazonaws.com/v1/repos/repo-man",

			mockStore: func(m *mocks.Mockstore) {
				m.EXPECT().GetEnvironment("my-app", "test").Return(
					&config.Environment{
						Name: "test",
					}, nil)
				m.EXPECT().GetEnvironment("my-app", "prod").Return(
					&config.Environment{
						Name: "prod",
					}, nil)
			},
			mockPrompt:       func(m *mocks.Mockprompter) {},
			mockRunner:       func(m *mocks.Mockrunner) {},
			mockSessProvider: func(m *mocks.MocksessionProvider) {},
			mockSelector:     func(m *mocks.MockpipelineEnvSelector) {},
		},
		"no flags, prompts for all input, success case for selecting URL": {
			inEnvironments:      []string{},
			inRepoURL:           "",
			inGitHubAccessToken: githubToken,
			inGitBranch:         "",
			buffer:              *bytes.NewBufferString("archer\tgit@github.com:goodGoose/bhaOS (fetch)\narcher\thttps://github.com/badGoose/chaOS (push)\narcher\tcodecommit::us-west-2://repo-man (fetch)\n"),
			mockRunner: func(m *mocks.Mockrunner) {
				m.EXPECT().Run(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil)
			},
			mockPrompt: func(m *mocks.Mockprompter) {
				m.EXPECT().SelectOne(pipelineSelectURLPrompt, gomock.Any(), gomock.Any(), gomock.Any()).Return(githubAnotherURL, nil).Times(1)
			},
			mockSelector: func(m *mocks.MockpipelineEnvSelector) {
				m.EXPECT().Environments(pipelineSelectEnvPrompt, gomock.Any(), "my-app", gomock.Any()).Return([]string{"test", "prod"}, nil)
			},
			mockStore: func(m *mocks.Mockstore) {
				m.EXPECT().GetEnvironment("my-app", "test").Return(&config.Environment{
					Name:   "test",
					Region: "us-west-2",
				}, nil)
				m.EXPECT().GetEnvironment("my-app", "prod").Return(&config.Environment{
					Name:   "prod",
					Region: "us-west-2",
				}, nil)
			},
			mockSessProvider: func(m *mocks.MocksessionProvider) {},
		},
		"returns error if fail to list environments": {
			inEnvironments: []string{},
			buffer:         *bytes.NewBufferString("archer\tgit@github.com:goodGoose/bhaOS (fetch)\narcher\thttps://github.com/badGoose/chaOS (push)\n"),
			mockRunner: func(m *mocks.Mockrunner) {
				m.EXPECT().Run(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil)
			},
			mockPrompt: func(m *mocks.Mockprompter) {
				m.EXPECT().SelectOne(pipelineSelectURLPrompt, gomock.Any(), gomock.Any(), gomock.Any()).Return(githubAnotherURL, nil).Times(1)
			},
			mockSelector: func(m *mocks.MockpipelineEnvSelector) {
				m.EXPECT().Environments(pipelineSelectEnvPrompt, gomock.Any(), "my-app", gomock.Any()).Return(nil, errors.New("some error"))
			},
			mockStore:        func(m *mocks.Mockstore) {},
			mockSessProvider: func(m *mocks.MocksessionProvider) {},

			expectedError: fmt.Errorf("select environments: some error"),
		},

		"returns error if fail to select URL": {
			inRepoURL:      "",
			inEnvironments: []string{},
			buffer:         *bytes.NewBufferString("archer\tgit@github.com:goodGoose/bhaOS (fetch)\narcher\thttps://github.com/badGoose/chaOS (push)\n"),

			mockSelector: func(m *mocks.MockpipelineEnvSelector) {},
			mockStore:    func(m *mocks.Mockstore) {},
			mockRunner: func(m *mocks.Mockrunner) {
				m.EXPECT().Run(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil)
			},
			mockPrompt: func(m *mocks.Mockprompter) {
				m.EXPECT().SelectOne(pipelineSelectURLPrompt, gomock.Any(), gomock.Any(), gomock.Any()).Return("", errors.New("some error")).Times(1)
			},
			mockSessProvider: func(m *mocks.MocksessionProvider) {},

			expectedError: fmt.Errorf("select URL: some error"),
		},
		"returns error if fail to get env config": {
			inRepoURL:      "",
			inEnvironments: []string{},
			buffer:         *bytes.NewBufferString("archer\tgit@github.com:goodGoose/bhaOS (fetch)\narcher\thttps://github.com/badGoose/chaOS (push)\n"),

			mockRunner: func(m *mocks.Mockrunner) {
				m.EXPECT().Run(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil)
			},
			mockPrompt: func(m *mocks.Mockprompter) {
				m.EXPECT().SelectOne(pipelineSelectURLPrompt, gomock.Any(), gomock.Any(), gomock.Any()).Return(githubAnotherURL, nil).Times(1)
			},
			mockSelector: func(m *mocks.MockpipelineEnvSelector) {
				m.EXPECT().Environments(pipelineSelectEnvPrompt, gomock.Any(), "my-app", gomock.Any()).Return([]string{"test", "prod"}, nil)
			},
			mockStore: func(m *mocks.Mockstore) {
				m.EXPECT().GetEnvironment("my-app", "test").Return(&config.Environment{
					Name:   "test",
					Region: "us-west-2",
				}, nil)
				m.EXPECT().GetEnvironment("my-app", "prod").Return(nil, errors.New("some error"))
			},
			mockSessProvider: func(m *mocks.MocksessionProvider) {},

			expectedError: fmt.Errorf("get config of environment prod: some error"),
		},
		"skip selector prompt if only one repo URL": {
			buffer: *bytes.NewBufferString("archer\tgit@github.com:goodGoose/bhaOS (fetch)\n"),

			mockSelector: func(m *mocks.MockpipelineEnvSelector) {
				m.EXPECT().Environments(pipelineSelectEnvPrompt, gomock.Any(), "my-app", gomock.Any()).Return([]string{"test", "prod"}, nil)
			},
			mockStore: func(m *mocks.Mockstore) {
				m.EXPECT().GetEnvironment("my-app", "test").Return(&config.Environment{
					Name:   "test",
					Region: "us-west-2",
				}, nil)
				m.EXPECT().GetEnvironment("my-app", "prod").Return(&config.Environment{
					Name:   "prod",
					Region: "us-west-2",
				}, nil)
			},
			mockRunner: func(m *mocks.Mockrunner) {
				m.EXPECT().Run(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil)
			},
			mockPrompt: func(m *mocks.Mockprompter) {
				m.EXPECT().SelectOne(pipelineSelectURLPrompt, gomock.Any(), gomock.Any(), gomock.Any()).Return("", nil).Times(0)
			},
			mockSessProvider: func(m *mocks.MocksessionProvider) {},
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			// GIVEN
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockPrompt := mocks.NewMockprompter(ctrl)
			mockRunner := mocks.NewMockrunner(ctrl)
			mocksSessProvider := mocks.NewMocksessionProvider(ctrl)
			mockSelector := mocks.NewMockpipelineEnvSelector(ctrl)
			mockStore := mocks.NewMockstore(ctrl)

			opts := &initPipelineOpts{
				initPipelineVars: initPipelineVars{
					appName:           "my-app",
					environments:      tc.inEnvironments,
					repoURL:           tc.inRepoURL,
					githubAccessToken: tc.inGitHubAccessToken,
				},
				prompt:       mockPrompt,
				runner:       mockRunner,
				sessProvider: mocksSessProvider,
				buffer:       tc.buffer,
				sel:          mockSelector,
				store:        mockStore,
			}

			tc.mockPrompt(mockPrompt)
			tc.mockRunner(mockRunner)
			tc.mockSessProvider(mocksSessProvider)
			tc.mockSelector(mockSelector)
			tc.mockStore(mockStore)

			// WHEN
			err := opts.Ask()

			// THEN
			if tc.expectedError != nil {
				require.EqualError(t, err, tc.expectedError.Error())
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestInitPipelineOpts_Execute(t *testing.T) {
	buildspecExistsErr := &workspace.ErrFileExists{FileName: "/buildspec.yml"}
	manifestExistsErr := &workspace.ErrFileExists{FileName: "/pipeline.yml"}
	testCases := map[string]struct {
		inEnvironments []string
		inEnvConfigs   []*config.Environment
		inGitHubToken  string
		inRepoURL      string
		inBranch       string
		inAppName      string

		mockSecretsManager          func(m *mocks.MocksecretsManager)
		mockWsWriter                func(m *mocks.MockwsPipelineWriter)
		mockParser                  func(m *templatemocks.MockParser)
		mockFileSystem              func(mockFS afero.Fs)
		mockRegionalResourcesGetter func(m *mocks.MockappResourcesGetter)
		mockStoreSvc                func(m *mocks.Mockstore)
		mockRunner                  func(m *mocks.Mockrunner)
		mockSessProvider            func(m *mocks.MocksessionProvider)
		buffer                      bytes.Buffer

		expectedBranch string
		expectedError  error
	}{
		"successfully detects local branch and sets it": {
			inEnvConfigs: []*config.Environment{
				{
					Name: "test",
				},
			},
			inRepoURL: "git@github.com:badgoose/goose.git",
			inAppName: "badgoose",

			mockSecretsManager: func(m *mocks.MocksecretsManager) {},
			mockWsWriter: func(m *mocks.MockwsPipelineWriter) {
				m.EXPECT().WritePipelineManifest(gomock.Any()).Return("/pipeline.yml", nil)
				m.EXPECT().WritePipelineBuildspec(gomock.Any()).Return("/buildspec.yml", nil)
			},
			mockParser: func(m *templatemocks.MockParser) {
				m.EXPECT().Parse(buildspecTemplatePath, gomock.Any()).Return(&template.Content{
					Buffer: bytes.NewBufferString("hello"),
				}, nil)
			},
			mockStoreSvc: func(m *mocks.Mockstore) {
				m.EXPECT().GetApplication("badgoose").Return(&config.Application{
					Name: "badgoose",
				}, nil)
			},
			mockRegionalResourcesGetter: func(m *mocks.MockappResourcesGetter) {
				m.EXPECT().GetRegionalAppResources(&config.Application{
					Name: "badgoose",
				}).Return([]*stack.AppRegionalResources{
					{
						Region:   "us-west-2",
						S3Bucket: "gooseBucket",
					},
				}, nil)
			},
			mockRunner: func(m *mocks.Mockrunner) {
				m.EXPECT().Run(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil)
			},
			buffer:         *bytes.NewBufferString("devBranch"),
			expectedBranch: "devBranch",
			expectedError:  nil,
		},
		"sets 'main' as branch name if error fetching it": {
			inEnvConfigs: []*config.Environment{
				{
					Name: "test",
				},
			},
			inRepoURL: "git@github.com:badgoose/goose.git",
			inAppName: "badgoose",

			mockSecretsManager: func(m *mocks.MocksecretsManager) {},
			mockWsWriter: func(m *mocks.MockwsPipelineWriter) {
				m.EXPECT().WritePipelineManifest(gomock.Any()).Return("/pipeline.yml", nil)
				m.EXPECT().WritePipelineBuildspec(gomock.Any()).Return("/buildspec.yml", nil)
			},
			mockParser: func(m *templatemocks.MockParser) {
				m.EXPECT().Parse(buildspecTemplatePath, gomock.Any()).Return(&template.Content{
					Buffer: bytes.NewBufferString("hello"),
				}, nil)
			},
			mockStoreSvc: func(m *mocks.Mockstore) {
				m.EXPECT().GetApplication("badgoose").Return(&config.Application{
					Name: "badgoose",
				}, nil)
			},
			mockRegionalResourcesGetter: func(m *mocks.MockappResourcesGetter) {
				m.EXPECT().GetRegionalAppResources(&config.Application{
					Name: "badgoose",
				}).Return([]*stack.AppRegionalResources{
					{
						Region:   "us-west-2",
						S3Bucket: "gooseBucket",
					},
				}, nil)
			},
			mockRunner: func(m *mocks.Mockrunner) {
				m.EXPECT().Run(gomock.Any(), gomock.Any(), gomock.Any()).Return(errors.New("some error"))
			},
			expectedBranch: "main",
			expectedError:  nil,
		},
		"creates secret and writes manifest and buildspec for GHV1 provider": {
			inEnvConfigs: []*config.Environment{
				{
					Name: "test",
				},
			},
			inGitHubToken: "hunter2",
			inRepoURL:     "git@github.com:badgoose/goose.git",
			inAppName:     "badgoose",

			mockSecretsManager: func(m *mocks.MocksecretsManager) {
				m.EXPECT().CreateSecret("github-token-badgoose-goose", "hunter2").Return("some-arn", nil)
			},
			mockWsWriter: func(m *mocks.MockwsPipelineWriter) {
				m.EXPECT().WritePipelineManifest(gomock.Any()).Return("/pipeline.yml", nil)
				m.EXPECT().WritePipelineBuildspec(gomock.Any()).Return("/buildspec.yml", nil)
			},
			mockParser: func(m *templatemocks.MockParser) {
				m.EXPECT().Parse(buildspecTemplatePath, gomock.Any()).Return(&template.Content{
					Buffer: bytes.NewBufferString("hello"),
				}, nil)
			},
			mockStoreSvc: func(m *mocks.Mockstore) {
				m.EXPECT().GetApplication("badgoose").Return(&config.Application{
					Name: "badgoose",
				}, nil)
			},
			mockRegionalResourcesGetter: func(m *mocks.MockappResourcesGetter) {
				m.EXPECT().GetRegionalAppResources(&config.Application{
					Name: "badgoose",
				}).Return([]*stack.AppRegionalResources{
					{
						Region:   "us-west-2",
						S3Bucket: "gooseBucket",
					},
				}, nil)
			},
			mockRunner: func(m *mocks.Mockrunner) {
				m.EXPECT().Run(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil)
			},
			expectedError:  nil,
			expectedBranch: "main",
		},
		"writes manifest and buildspec for GH(v2) provider": {
			inEnvConfigs: []*config.Environment{
				{
					Name: "test",
				},
			},
			inRepoURL: "git@github.com:badgoose/goose.git",
			inAppName: "badgoose",

			mockSecretsManager: func(m *mocks.MocksecretsManager) {},
			mockWsWriter: func(m *mocks.MockwsPipelineWriter) {
				m.EXPECT().WritePipelineManifest(gomock.Any()).Return("/pipeline.yml", nil)
				m.EXPECT().WritePipelineBuildspec(gomock.Any()).Return("/buildspec.yml", nil)
			},
			mockParser: func(m *templatemocks.MockParser) {
				m.EXPECT().Parse(buildspecTemplatePath, gomock.Any()).Return(&template.Content{
					Buffer: bytes.NewBufferString("hello"),
				}, nil)
			},
			mockStoreSvc: func(m *mocks.Mockstore) {
				m.EXPECT().GetApplication("badgoose").Return(&config.Application{
					Name: "badgoose",
				}, nil)
			},
			mockRegionalResourcesGetter: func(m *mocks.MockappResourcesGetter) {
				m.EXPECT().GetRegionalAppResources(&config.Application{
					Name: "badgoose",
				}).Return([]*stack.AppRegionalResources{
					{
						Region:   "us-west-2",
						S3Bucket: "gooseBucket",
					},
				}, nil)
			},
			mockRunner: func(m *mocks.Mockrunner) {
				m.EXPECT().Run(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil)
			},
			expectedError:  nil,
			expectedBranch: "main",
		},
		"writes manifest and buildspec for CC provider": {
			inEnvConfigs: []*config.Environment{
				{
					Name: "test",
				},
			},
			inRepoURL: "https://git-codecommit.us-west-2.amazonaws.com/v1/repos/goose",
			inAppName: "badgoose",

			mockSecretsManager: func(m *mocks.MocksecretsManager) {},
			mockWsWriter: func(m *mocks.MockwsPipelineWriter) {
				m.EXPECT().WritePipelineManifest(gomock.Any()).Return("/pipeline.yml", nil)
				m.EXPECT().WritePipelineBuildspec(gomock.Any()).Return("/buildspec.yml", nil)
			},
			mockParser: func(m *templatemocks.MockParser) {
				m.EXPECT().Parse(buildspecTemplatePath, gomock.Any()).Return(&template.Content{
					Buffer: bytes.NewBufferString("hello"),
				}, nil)
			},
			mockStoreSvc: func(m *mocks.Mockstore) {
				m.EXPECT().GetApplication("badgoose").Return(&config.Application{
					Name: "badgoose",
				}, nil)
			},
			mockRegionalResourcesGetter: func(m *mocks.MockappResourcesGetter) {
				m.EXPECT().GetRegionalAppResources(&config.Application{
					Name: "badgoose",
				}).Return([]*stack.AppRegionalResources{
					{
						Region:   "us-west-2",
						S3Bucket: "gooseBucket",
					},
				}, nil)
			},
			mockRunner: func(m *mocks.Mockrunner) {
				m.EXPECT().Run(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil)
			},
			mockSessProvider: func(m *mocks.MocksessionProvider) {
				m.EXPECT().Default().Return(&session.Session{
					Config: &aws.Config{
						Region: aws.String("us-west-2"),
					},
				}, nil)
			},
			expectedError:  nil,
			expectedBranch: "main",
		},
		"writes manifest and buildspec for BB provider": {
			inEnvConfigs: []*config.Environment{
				{
					Name: "test",
				},
			},
			inRepoURL: "https://huanjani@bitbucket.org/badgoose/goose.git",
			inAppName: "badgoose",

			mockSecretsManager: func(m *mocks.MocksecretsManager) {},
			mockWsWriter: func(m *mocks.MockwsPipelineWriter) {
				m.EXPECT().WritePipelineManifest(gomock.Any()).Return("/pipeline.yml", nil)
				m.EXPECT().WritePipelineBuildspec(gomock.Any()).Return("/buildspec.yml", nil)
			},
			mockParser: func(m *templatemocks.MockParser) {
				m.EXPECT().Parse(buildspecTemplatePath, gomock.Any()).Return(&template.Content{
					Buffer: bytes.NewBufferString("hello"),
				}, nil)
			},
			mockStoreSvc: func(m *mocks.Mockstore) {
				m.EXPECT().GetApplication("badgoose").Return(&config.Application{
					Name: "badgoose",
				}, nil)
			},
			mockRegionalResourcesGetter: func(m *mocks.MockappResourcesGetter) {
				m.EXPECT().GetRegionalAppResources(&config.Application{
					Name: "badgoose",
				}).Return([]*stack.AppRegionalResources{
					{
						Region:   "us-west-2",
						S3Bucket: "gooseBucket",
					},
				}, nil)
			},
			mockRunner: func(m *mocks.Mockrunner) {
				m.EXPECT().Run(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil)
			},
			expectedError:  nil,
			expectedBranch: "main",
		},
		"does not return an error if secret already exists": {
			inEnvConfigs: []*config.Environment{
				{
					Name: "test",
				},
			},
			inGitHubToken: "hunter2",
			inRepoURL:     "git@github.com:badgoose/goose.git",
			inAppName:     "badgoose",

			mockSecretsManager: func(m *mocks.MocksecretsManager) {
				existsErr := &secretsmanager.ErrSecretAlreadyExists{}
				m.EXPECT().CreateSecret("github-token-badgoose-goose", "hunter2").Return("", existsErr)
			},
			mockWsWriter: func(m *mocks.MockwsPipelineWriter) {
				m.EXPECT().WritePipelineManifest(gomock.Any()).Return("/pipeline.yml", nil)
				m.EXPECT().WritePipelineBuildspec(gomock.Any()).Return("/buildspec.yml", nil)
			},
			mockParser: func(m *templatemocks.MockParser) {
				m.EXPECT().Parse(buildspecTemplatePath, gomock.Any()).Return(&template.Content{
					Buffer: bytes.NewBufferString("hello"),
				}, nil)
			},
			mockStoreSvc: func(m *mocks.Mockstore) {
				m.EXPECT().GetApplication("badgoose").Return(&config.Application{
					Name: "badgoose",
				}, nil)
			},
			mockRegionalResourcesGetter: func(m *mocks.MockappResourcesGetter) {
				m.EXPECT().GetRegionalAppResources(&config.Application{
					Name: "badgoose",
				}).Return([]*stack.AppRegionalResources{
					{
						Region:   "us-west-2",
						S3Bucket: "gooseBucket",
					},
				}, nil)
			},
			mockRunner: func(m *mocks.Mockrunner) {
				m.EXPECT().Run(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil)
			},
			expectedError:  nil,
			expectedBranch: "main",
		},
		"returns an error if can't write manifest": {
			inEnvConfigs: []*config.Environment{
				{
					Name: "test",
					Prod: false,
				},
			},
			inGitHubToken: "hunter2",
			inRepoURL:     "git@github.com:badgoose/goose.git",
			inAppName:     "badgoose",

			mockSecretsManager: func(m *mocks.MocksecretsManager) {
				m.EXPECT().CreateSecret("github-token-badgoose-goose", "hunter2").Return("some-arn", nil)
			},
			mockWsWriter: func(m *mocks.MockwsPipelineWriter) {
				m.EXPECT().WritePipelineManifest(gomock.Any()).Return("", errors.New("some error"))
			},
			mockParser:                  func(m *templatemocks.MockParser) {},
			mockStoreSvc:                func(m *mocks.Mockstore) {},
			mockRegionalResourcesGetter: func(m *mocks.MockappResourcesGetter) {},
			mockRunner: func(m *mocks.Mockrunner) {
				m.EXPECT().Run(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil)
			},
			expectedError: errors.New("write pipeline manifest to workspace: some error"),
		},
		"returns an error if application cannot be retrieved": {
			inEnvConfigs: []*config.Environment{
				{
					Name: "test",
				},
			},
			inGitHubToken: "hunter2",
			inRepoURL:     "git@github.com:badgoose/goose.git",
			inAppName:     "badgoose",

			mockSecretsManager: func(m *mocks.MocksecretsManager) {
				m.EXPECT().CreateSecret("github-token-badgoose-goose", "hunter2").Return("some-arn", nil)
			},
			mockWsWriter: func(m *mocks.MockwsPipelineWriter) {
				m.EXPECT().WritePipelineManifest(gomock.Any()).Return("/pipeline.yml", nil)
			},
			mockParser: func(m *templatemocks.MockParser) {},
			mockStoreSvc: func(m *mocks.Mockstore) {
				m.EXPECT().GetApplication("badgoose").Return(nil, errors.New("some error"))
			},
			mockRegionalResourcesGetter: func(m *mocks.MockappResourcesGetter) {},
			mockRunner: func(m *mocks.Mockrunner) {
				m.EXPECT().Run(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil)
			},
			expectedError: errors.New("get application badgoose: some error"),
		},
		"returns an error if can't get regional application resources": {
			inEnvConfigs: []*config.Environment{
				{
					Name: "test",
				},
			},
			inGitHubToken: "hunter2",
			inRepoURL:     "git@github.com:badgoose/goose.git",
			inAppName:     "badgoose",

			mockSecretsManager: func(m *mocks.MocksecretsManager) {
				m.EXPECT().CreateSecret("github-token-badgoose-goose", "hunter2").Return("some-arn", nil)
			},
			mockWsWriter: func(m *mocks.MockwsPipelineWriter) {
				m.EXPECT().WritePipelineManifest(gomock.Any()).Return("/pipeline.yml", nil)
			},
			mockParser: func(m *templatemocks.MockParser) {},
			mockStoreSvc: func(m *mocks.Mockstore) {
				m.EXPECT().GetApplication("badgoose").Return(&config.Application{
					Name: "badgoose",
				}, nil)
			},
			mockRegionalResourcesGetter: func(m *mocks.MockappResourcesGetter) {
				m.EXPECT().GetRegionalAppResources(&config.Application{
					Name: "badgoose",
				}).Return(nil, errors.New("some error"))
			},
			mockRunner: func(m *mocks.Mockrunner) {
				m.EXPECT().Run(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil)
			},
			expectedError: fmt.Errorf("get regional application resources: some error"),
		},
		"returns an error if buildspec cannot be parsed": {
			inEnvConfigs: []*config.Environment{
				{
					Name: "test",
				},
			},
			inGitHubToken: "hunter2",
			inRepoURL:     "git@github.com:badgoose/goose.git",
			inAppName:     "badgoose",

			mockSecretsManager: func(m *mocks.MocksecretsManager) {
				m.EXPECT().CreateSecret("github-token-badgoose-goose", "hunter2").Return("some-arn", nil)
			},
			mockWsWriter: func(m *mocks.MockwsPipelineWriter) {
				m.EXPECT().WritePipelineManifest(gomock.Any()).Return("/pipeline.yml", nil)
				m.EXPECT().WritePipelineBuildspec(gomock.Any()).Times(0)
			},
			mockParser: func(m *templatemocks.MockParser) {
				m.EXPECT().Parse(buildspecTemplatePath, gomock.Any()).Return(nil, errors.New("some error"))
			},
			mockStoreSvc: func(m *mocks.Mockstore) {
				m.EXPECT().GetApplication("badgoose").Return(&config.Application{
					Name: "badgoose",
				}, nil)
			},
			mockRegionalResourcesGetter: func(m *mocks.MockappResourcesGetter) {
				m.EXPECT().GetRegionalAppResources(&config.Application{
					Name: "badgoose",
				}).Return([]*stack.AppRegionalResources{
					{
						Region:   "us-west-2",
						S3Bucket: "gooseBucket",
					},
				}, nil)
			},
			mockRunner: func(m *mocks.Mockrunner) {
				m.EXPECT().Run(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil)
			},
			expectedError: errors.New("some error"),
		},
		"does not return an error if buildspec and manifest already exists": {
			inEnvConfigs: []*config.Environment{
				{
					Name: "test",
				},
			},
			inGitHubToken: "hunter2",
			inRepoURL:     "git@github.com:badgoose/goose.git",
			inAppName:     "badgoose",

			mockSecretsManager: func(m *mocks.MocksecretsManager) {
				m.EXPECT().CreateSecret("github-token-badgoose-goose", "hunter2").Return("some-arn", nil)
			},
			mockWsWriter: func(m *mocks.MockwsPipelineWriter) {
				m.EXPECT().WritePipelineManifest(gomock.Any()).Return("", manifestExistsErr)
				m.EXPECT().WritePipelineBuildspec(gomock.Any()).Return("", buildspecExistsErr)
			},
			mockParser: func(m *templatemocks.MockParser) {
				m.EXPECT().Parse(buildspecTemplatePath, gomock.Any()).Return(&template.Content{
					Buffer: bytes.NewBufferString("hello"),
				}, nil)
			},
			mockStoreSvc: func(m *mocks.Mockstore) {
				m.EXPECT().GetApplication("badgoose").Return(&config.Application{
					Name: "badgoose",
				}, nil)
			},
			mockRegionalResourcesGetter: func(m *mocks.MockappResourcesGetter) {
				m.EXPECT().GetRegionalAppResources(&config.Application{
					Name: "badgoose",
				}).Return([]*stack.AppRegionalResources{
					{
						Region:   "us-west-2",
						S3Bucket: "gooseBucket",
					},
				}, nil)
			},
			mockRunner: func(m *mocks.Mockrunner) {
				m.EXPECT().Run(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil)
			},
			expectedError:  nil,
			expectedBranch: "main",
		},
		"returns an error if can't write buildspec": {
			inEnvConfigs: []*config.Environment{
				{
					Name: "test",
					Prod: false,
				},
			},
			inGitHubToken: "hunter2",
			inRepoURL:     "git@github.com:badgoose/goose.git",
			inAppName:     "badgoose",

			mockSecretsManager: func(m *mocks.MocksecretsManager) {
				m.EXPECT().CreateSecret("github-token-badgoose-goose", "hunter2").Return("some-arn", nil)
			},
			mockWsWriter: func(m *mocks.MockwsPipelineWriter) {
				m.EXPECT().WritePipelineManifest(gomock.Any()).Return("/pipeline.yml", nil)
				m.EXPECT().WritePipelineBuildspec(gomock.Any()).Return("", errors.New("some error"))
			},
			mockParser: func(m *templatemocks.MockParser) {
				m.EXPECT().Parse(buildspecTemplatePath, gomock.Any()).Return(&template.Content{
					Buffer: bytes.NewBufferString("hello"),
				}, nil)
			},
			mockStoreSvc: func(m *mocks.Mockstore) {
				m.EXPECT().GetApplication("badgoose").Return(&config.Application{
					Name: "badgoose",
				}, nil)
			},
			mockRegionalResourcesGetter: func(m *mocks.MockappResourcesGetter) {
				m.EXPECT().GetRegionalAppResources(&config.Application{
					Name: "badgoose",
				}).Return([]*stack.AppRegionalResources{
					{
						Region:   "us-west-2",
						S3Bucket: "gooseBucket",
					},
				}, nil)
			},
			mockRunner: func(m *mocks.Mockrunner) {
				m.EXPECT().Run(gomock.Any(), gomock.Any(), gomock.Any()).Return(nil)
			},
			expectedError: fmt.Errorf("write buildspec to workspace: some error"),
		},
		"returns error when repository URL is not from a supported git provider": {
			inRepoURL:     "https://gitlab.company.com/group/project.git",
			inBranch:      "main",
			inAppName:     "demo",
			expectedError: errors.New("repository https://gitlab.company.com/group/project.git must be from a supported provider: GitHub, CodeCommit or Bitbucket"),
		},
		"returns error when GitHub repository URL is of unknown format": {
			inRepoURL:     "thisisnotevenagithub.comrepository",
			inBranch:      "main",
			inAppName:     "demo",
			expectedError: errors.New("unable to parse the GitHub repository owner and name from thisisnotevenagithub.comrepository: please pass the repository URL with the format `--url https://github.com/{owner}/{repositoryName}`"),
		},
		"returns error when CodeCommit repository URL is of unknown format": {
			inRepoURL:     "git-codecommitus-west-2amazonaws.com",
			inBranch:      "main",
			inAppName:     "demo",
			expectedError: errors.New("unknown CodeCommit URL format: git-codecommitus-west-2amazonaws.com"),
		},
		"returns error when CodeCommit repository contains unknown region": {
			inRepoURL:     "codecommit::us-mess-2://repo-man",
			inBranch:      "main",
			inAppName:     "demo",
			expectedError: errors.New("unable to parse the AWS region from codecommit::us-mess-2://repo-man"),
		},
		"returns error when CodeCommit repository region does not match pipeline's region": {
			inRepoURL: "codecommit::us-west-2://repo-man",
			inBranch:  "main",
			inAppName: "demo",
			mockSessProvider: func(m *mocks.MocksessionProvider) {
				m.EXPECT().Default().Return(&session.Session{
					Config: &aws.Config{
						Region: aws.String("us-east-1"),
					},
				}, nil)
			},
			expectedError: errors.New("repository repo-man is in us-west-2, but app demo is in us-east-1; they must be in the same region"),
		},
		"returns error when Bitbucket repository URL is of unknown format": {
			inRepoURL:     "bitbucket.org",
			inBranch:      "main",
			inAppName:     "demo",
			expectedError: errors.New("unable to parse the Bitbucket repository name from bitbucket.org"),
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			// GIVEN
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			mockSecretsManager := mocks.NewMocksecretsManager(ctrl)
			mockWriter := mocks.NewMockwsPipelineWriter(ctrl)
			mockParser := templatemocks.NewMockParser(ctrl)
			mockRegionalResourcesGetter := mocks.NewMockappResourcesGetter(ctrl)
			mockstore := mocks.NewMockstore(ctrl)
			mockRunner := mocks.NewMockrunner(ctrl)
			mockSessProvider := mocks.NewMocksessionProvider(ctrl)

			if tc.mockSecretsManager != nil {
				tc.mockSecretsManager(mockSecretsManager)
			}
			if tc.mockWsWriter != nil {
				tc.mockWsWriter(mockWriter)
			}
			if tc.mockParser != nil {
				tc.mockParser(mockParser)
			}
			if tc.mockRegionalResourcesGetter != nil {
				tc.mockRegionalResourcesGetter(mockRegionalResourcesGetter)
			}
			if tc.mockStoreSvc != nil {
				tc.mockStoreSvc(mockstore)
			}
			if tc.mockRunner != nil {
				tc.mockRunner(mockRunner)
			}
			if tc.mockSessProvider != nil {
				tc.mockSessProvider(mockSessProvider)
			}
			memFs := &afero.Afero{Fs: afero.NewMemMapFs()}

			opts := &initPipelineOpts{
				initPipelineVars: initPipelineVars{
					githubAccessToken: tc.inGitHubToken,
					appName:           tc.inAppName,
					repoBranch:        tc.inBranch,
					repoURL:           tc.inRepoURL,
				},

				secretsmanager: mockSecretsManager,
				cfnClient:      mockRegionalResourcesGetter,
				sessProvider:   mockSessProvider,
				store:          mockstore,
				workspace:      mockWriter,
				parser:         mockParser,
				runner:         mockRunner,
				fs:             memFs,
				buffer:         tc.buffer,
				envConfigs:     tc.inEnvConfigs,
			}

			// WHEN
			err := opts.Execute()

			// THEN
			if tc.expectedError != nil {
				require.EqualError(t, err, tc.expectedError.Error())
			} else {
				require.NoError(t, err)
				require.Equal(t, tc.expectedBranch, opts.repoBranch)
			}
		})
	}
}

func TestInitPipelineOpts_pipelineName(t *testing.T) {
	testCases := map[string]struct {
		inRepoName string
		inAppName  string

		expected    string
		expectedErr error
	}{
		"generates pipeline name": {
			inAppName:  "goodmoose",
			inRepoName: "repo-man",

			expected: "pipeline-goodmoose-repo-man",
		},
		"generates and truncates pipeline name if it exceeds 100 characters": {
			inAppName:  "goodmoose01234567820123456783012345678401234567850",
			inRepoName: "repo-man101234567820123456783012345678401234567850",

			expected: "pipeline-goodmoose01234567820123456783012345678401234567850-repo-man10123456782012345678301234567840",
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			// GIVEN
			opts := &initPipelineOpts{
				initPipelineVars: initPipelineVars{
					appName: tc.inAppName,
				},
				repoName: tc.inRepoName,
			}

			// WHEN
			actual := opts.pipelineName()

			// THEN
			require.Equal(t, tc.expected, actual)
		})
	}
}

func TestInitPipelineOpts_parseGitRemoteResult(t *testing.T) {
	testCases := map[string]struct {
		inRemoteResult string

		expectedURLs  []string
		expectedError error
	}{
		"matched format": {
			inRemoteResult: `badgoose	git@github.com:badgoose/grit.git (fetch)
badgoose	https://github.com/badgoose/cli.git (fetch)
origin	https://github.com/koke/grit (fetch)
koke	git://github.com/koke/grit.git (push)
https	https://git-codecommit.us-west-2.amazonaws.com/v1/repos/aws-sample (fetch)
fed	codecommit::us-west-2://aws-sample (fetch)
ssh	ssh://git-codecommit.us-west-2.amazonaws.com/v1/repos/aws-sample (push)
bb	https://huanjani@bitbucket.org/huanjani/aws-copilot-sample-service.git (push)`,

			expectedURLs: []string{"git@github.com:badgoose/grit", "https://github.com/badgoose/cli", "https://github.com/koke/grit", "git://github.com/koke/grit", "https://git-codecommit.us-west-2.amazonaws.com/v1/repos/aws-sample", "codecommit::us-west-2://aws-sample", "ssh://git-codecommit.us-west-2.amazonaws.com/v1/repos/aws-sample", "https://huanjani@bitbucket.org/huanjani/aws-copilot-sample-service"},
		},
		"don't add to URL list if it is not a GitHub or CodeCommit or Bitbucket URL": {
			inRemoteResult: `badgoose	verybad@gitlab.com/whatever (fetch)`,

			expectedURLs: []string{},
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			// GIVEN
			opts := &initPipelineOpts{}

			// WHEN
			urls, err := opts.parseGitRemoteResult(tc.inRemoteResult)
			// THEN
			if tc.expectedError != nil {
				require.EqualError(t, err, tc.expectedError.Error())
			} else {
				require.ElementsMatch(t, tc.expectedURLs, urls)
			}
		})
	}
}

func TestInitPipelineGHRepoURL_parse(t *testing.T) {
	testCases := map[string]struct {
		inRepoURL ghRepoURL

		expectedDetails ghRepoDetails
		expectedError   error
	}{
		"successfully parses name without .git suffix": {
			inRepoURL: "https://github.com/badgoose/cli",

			expectedDetails: ghRepoDetails{
				name:  "cli",
				owner: "badgoose",
			},
		},
		"successfully parses repo name with .git suffix": {
			inRepoURL: "https://github.com/koke/grit.git",

			expectedDetails: ghRepoDetails{
				name:  "grit",
				owner: "koke",
			},
		},
		"returns an error if it is not a github URL": {
			inRepoURL: "https://git-codecommit.us-east-1.amazonaws.com/v1/repos/whatever",

			expectedError: fmt.Errorf("unable to parse the GitHub repository owner and name from https://git-codecommit.us-east-1.amazonaws.com/v1/repos/whatever: please pass the repository URL with the format `--url https://github.com/{owner}/{repositoryName}`"),
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			// WHEN
			details, err := ghRepoURL.parse(tc.inRepoURL)

			// THEN
			if tc.expectedError != nil {
				require.EqualError(t, err, tc.expectedError.Error())
			} else {
				require.Equal(t, tc.expectedDetails, details)
			}
		})
	}
}

func TestInitPipelineCCRepoURL_parse(t *testing.T) {
	testCases := map[string]struct {
		inRepoURL ccRepoURL

		expectedDetails ccRepoDetails
		expectedError   error
	}{
		"successfully parses https url": {
			inRepoURL: "https://git-codecommit.sa-east-1.amazonaws.com/v1/repos/aws-sample",

			expectedDetails: ccRepoDetails{
				name:   "aws-sample",
				region: "sa-east-1",
			},
		},
		"successfully parses ssh url": {
			inRepoURL: "ssh://git-codecommit.us-east-2.amazonaws.com/v1/repos/aws-sample",

			expectedDetails: ccRepoDetails{
				name:   "aws-sample",
				region: "us-east-2",
			},
		},
		"successfully parses federated (GRC) url": {
			inRepoURL: "codecommit::us-gov-west-1://aws-sample",

			expectedDetails: ccRepoDetails{
				name:   "aws-sample",
				region: "us-gov-west-1",
			},
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			// WHEN
			details, err := ccRepoURL.parse(tc.inRepoURL)

			// THEN
			if tc.expectedError != nil {
				require.EqualError(t, err, tc.expectedError.Error())
			} else {
				require.Equal(t, tc.expectedDetails, details)
			}
		})
	}
}

func TestInitPipelineBBRepoURL_parse(t *testing.T) {
	testCases := map[string]struct {
		inRepoURL bbRepoURL

		expectedDetails bbRepoDetails
		expectedError   error
	}{
		"successfully parses https url": {
			inRepoURL: "https://huanjani@bitbucket.org/huanjani/aws-copilot-sample-service",

			expectedDetails: bbRepoDetails{
				name:  "aws-copilot-sample-service",
				owner: "huanjani",
			},
		},
		"successfully parses ssh url": {
			inRepoURL: "ssh://git@bitbucket.org:huanjani/aws-copilot-sample-service",

			expectedDetails: bbRepoDetails{
				name:  "aws-copilot-sample-service",
				owner: "huanjani",
			},
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			// WHEN
			details, err := bbRepoURL.parse(tc.inRepoURL)

			// THEN
			if tc.expectedError != nil {
				require.EqualError(t, err, tc.expectedError.Error())
			} else {
				require.Equal(t, tc.expectedDetails, details)
			}
		})
	}
}
