package telegram

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"clever-connect/internal/logger"
	"clever-connect/internal/models"

	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/telegram/dcs"
	"github.com/gotd/td/tg"
)

var (
	authMu       sync.Mutex
	phoneChan    chan string
	codeChan     chan string
	passwordChan chan string
	errChan      chan error
	successChan  chan struct{}
	pwReqChan    chan struct{}
	codeSentChan chan struct{}
	authCtx      context.Context
	authCancel   context.CancelFunc
)

func InitAuthFlow() {
	authMu.Lock()
	defer authMu.Unlock()

	if authCancel != nil {
		authCancel()
	}

	phoneChan = make(chan string, 1)
	codeChan = make(chan string, 1)
	passwordChan = make(chan string, 1)
	errChan = make(chan error, 1)
	successChan = make(chan struct{}, 1)
	pwReqChan = make(chan struct{}, 1)
	codeSentChan = make(chan struct{}, 1)

	authCtx, authCancel = context.WithCancel(context.Background())
}

// customAuthenticator implements auth.UserAuthenticator
type customAuthenticator struct {
	phone        string
	codeChan     chan string
	passwordChan chan string
	pwReqChan    chan struct{}
	codeSentChan chan struct{}
}

func (a customAuthenticator) Phone(ctx context.Context) (string, error) {
	return a.phone, nil
}

func (a customAuthenticator) Password(ctx context.Context) (string, error) {
	select {
	case a.pwReqChan <- struct{}{}:
	default:
	}

	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case password := <-a.passwordChan:
		return password, nil
	}
}

func (a customAuthenticator) Code(ctx context.Context, sentCode *tg.AuthSentCode) (string, error) {
	select {
	case a.codeSentChan <- struct{}{}:
	default:
	}

	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case code := <-a.codeChan:
		return code, nil
	}
}

func (a customAuthenticator) AcceptTermsOfService(ctx context.Context, tos tg.HelpTermsOfService) error {
	return nil
}

func (a customAuthenticator) SignUp(ctx context.Context) (auth.UserInfo, error) {
	return auth.UserInfo{}, errors.New("signup not supported")
}

func StartAuthClient(phoneNumber string, cfg *models.TelegramConfig) {
	authMu.Lock()
	ctx := authCtx
	authMu.Unlock()

	go func() {
		appID := cfg.AppID
		if appID == 0 {
			appID = PublicAppID
		}
		appHash := cfg.AppHash
		if appHash == "" {
			appHash = PublicAppHash
		}

		sessionDir := filepath.Join("./data/manager", ".telegram")
		_ = os.MkdirAll(sessionDir, 0755)
		sessionPath := filepath.Join(sessionDir, "session.json")
		// Clear any existing session to start fresh login
		_ = os.Remove(sessionPath)

		opts := telegram.Options{
			SessionStorage: &telegram.FileSessionStorage{
				Path: sessionPath,
			},
		}

		if cfg.MTProtoServer != "" {
			if strings.Contains(cfg.MTProtoServer, "149.154.167.40") || strings.Contains(strings.ToLower(cfg.MTProtoServer), "test") {
				opts.DCList = dcs.Test()
			}
		}

		client := telegram.NewClient(appID, appHash, opts)

		authenticator := customAuthenticator{
			phone:        phoneNumber,
			codeChan:     codeChan,
			passwordChan: passwordChan,
			pwReqChan:    pwReqChan,
			codeSentChan: codeSentChan,
		}

		err := client.Run(ctx, func(ctx context.Context) error {
			flow := auth.NewFlow(authenticator, auth.SendCodeOptions{})

			if err := client.Auth().IfNecessary(ctx, flow); err != nil {
				return err
			}

			// Auth succeeded
			close(successChan)
			return nil
		})

		if err != nil && !errors.Is(err, context.Canceled) {
			logger.Error("Telegram", "Interactive auth client run failed", "error", err)
			select {
			case errChan <- err:
			default:
			}
		}

		if err == nil {
			// Interactive User Account Verification succeeded — mirror the
			// fresh MTProto session into the database so the account stays
			// verified across container restarts and the engine is always
			// ready to start without repeating verification.
			SaveUserSessionData()
		}
	}()
}

func GetCodeChan() chan string {
	authMu.Lock()
	defer authMu.Unlock()
	return codeChan
}

func GetPasswordChan() chan string {
	authMu.Lock()
	defer authMu.Unlock()
	return passwordChan
}

func GetSuccessChan() chan struct{} {
	authMu.Lock()
	defer authMu.Unlock()
	return successChan
}

func GetPwReqChan() chan struct{} {
	authMu.Lock()
	defer authMu.Unlock()
	return pwReqChan
}

func GetErrChan() chan error {
	authMu.Lock()
	defer authMu.Unlock()
	return errChan
}

func GetCodeSentChan() chan struct{} {
	authMu.Lock()
	defer authMu.Unlock()
	return codeSentChan
}
