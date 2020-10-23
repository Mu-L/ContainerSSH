package ssh

import (
	"context"
	"fmt"
	"github.com/containerssh/containerssh/metrics"

	"encoding/base64"

	"github.com/containerssh/containerssh/backend"
	"github.com/containerssh/containerssh/config"
	configurationClient "github.com/containerssh/containerssh/config/client"
	"github.com/containerssh/containerssh/config/util"
	"github.com/containerssh/containerssh/log"
	"github.com/containerssh/containerssh/protocol"
	"github.com/containerssh/containerssh/ssh/server"

	"github.com/qdm12/reprint"
	"golang.org/x/crypto/ssh"
)

type ChannelHandlerFactory interface {
	Make(appConfig *config.AppConfig) *ChannelHandler
}

type ChannelHandler struct {
	appConfig                    *config.AppConfig
	backendRegistry              *backend.Registry
	configClient                 configurationClient.ConfigClient
	channelRequestHandlerFactory ChannelRequestHandlerFactory
	logger                       log.Logger
	metric                       *metrics.MetricCollector
}

func NewChannelHandler(
	appConfig *config.AppConfig,
	backendRegistry *backend.Registry,
	configClient configurationClient.ConfigClient,
	channelRequestHandlerFactory ChannelRequestHandlerFactory,
	logger log.Logger,
	metric *metrics.MetricCollector,
) *ChannelHandler {
	return &ChannelHandler{
		appConfig:                    appConfig,
		backendRegistry:              backendRegistry,
		configClient:                 configClient,
		channelRequestHandlerFactory: channelRequestHandlerFactory,
		logger:                       logger,
		metric:                       metric,
	}
}

func (handler *ChannelHandler) OnChannel(
	_ context.Context,
	connection ssh.ConnMetadata,
	channelType string,
	_ []byte,
) (server.ChannelRequestHandler, *server.ChannelRejection) {
	if channelType != "session" {
		return nil, &server.ChannelRejection{
			RejectionReason:  ssh.UnknownChannelType,
			RejectionMessage: "unknown channel type",
		}
	}

	actualConfig := config.AppConfig{}
	err := reprint.FromTo(handler.appConfig, &actualConfig)
	if err != nil {
		handler.logger.WarningF("failed to copy application config (%v)", err)
		return nil, &server.ChannelRejection{
			RejectionReason:  ssh.ResourceShortage,
			RejectionMessage: "failed to create config",
		}
	}

	if handler.configClient != nil {
		configResponse, err := handler.configClient.GetConfig(protocol.ConfigRequest{
			Username:  connection.User(),
			SessionId: base64.StdEncoding.EncodeToString(connection.SessionID()),
		})
		if err != nil {
			handler.logger.DebugE(err)
			return nil, &server.ChannelRejection{
				RejectionReason:  ssh.ResourceShortage,
				RejectionMessage: fmt.Sprintf("internal error while calling the config server: %s", err),
			}
		}
		newConfig, err := util.Merge(&configResponse.Config, &actualConfig)
		if err != nil {
			handler.logger.DebugE(err)
			return nil, &server.ChannelRejection{
				RejectionReason:  ssh.ResourceShortage,
				RejectionMessage: fmt.Sprintf("failed to merge config"),
			}
		}
		actualConfig = *newConfig
	}

	containerBackend, err := handler.backendRegistry.GetBackend(actualConfig.Backend)
	if err != nil {
		handler.logger.DebugE(err)
		return nil, &server.ChannelRejection{
			RejectionReason:  ssh.ResourceShortage,
			RejectionMessage: fmt.Sprintf("no such backend"),
		}
	}

	backendSession, err := containerBackend.CreateSession(
		string(connection.SessionID()),
		connection.User(),
		&actualConfig,
		handler.logger,
		handler.metric,
	)
	if err != nil {
		handler.logger.DebugE(err)
		return nil, &server.ChannelRejection{
			RejectionReason:  ssh.ResourceShortage,
			RejectionMessage: fmt.Sprintf("internal error while creating backend"),
		}
	}

	return handler.channelRequestHandlerFactory.Make(backendSession), nil
}

type channelHandlerFactory struct {
	backendRegistry              *backend.Registry
	configClient                 configurationClient.ConfigClient
	channelRequestHandlerFactory ChannelRequestHandlerFactory
	logger                       log.Logger
	loggerFactory                log.LoggerFactory
	metric                       *metrics.MetricCollector
}

func (factory *channelHandlerFactory) Make(appConfig *config.AppConfig) *ChannelHandler {
	logConfig, err := log.NewConfig(appConfig.Log.Level)
	logger := factory.logger
	if err != nil {
		factory.logger.WarningF("invalid log level (%s) using default logger", appConfig.Log.Level)
	} else {
		logger = factory.loggerFactory.Make(logConfig)
	}

	return NewChannelHandler(
		appConfig,
		factory.backendRegistry,
		factory.configClient,
		factory.channelRequestHandlerFactory,
		logger,
		factory.metric,
	)
}

func NewDefaultChannelHandlerFactory(
	backendRegistry *backend.Registry,
	configClient configurationClient.ConfigClient,
	channelRequestHandlerFactory ChannelRequestHandlerFactory,
	logger log.Logger,
	loggerFactory log.LoggerFactory,
	metric *metrics.MetricCollector,
) ChannelHandlerFactory {
	return &channelHandlerFactory{
		backendRegistry:              backendRegistry,
		configClient:                 configClient,
		channelRequestHandlerFactory: channelRequestHandlerFactory,
		logger:                       logger,
		loggerFactory:                loggerFactory,
		metric:                       metric,
	}
}
