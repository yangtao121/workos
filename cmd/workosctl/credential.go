package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"connectrpc.com/connect"

	credentialv1 "github.com/yangtao121/workos/gen/go/workos/credential/v1"
	"github.com/yangtao121/workos/gen/go/workos/credential/v1/credentialv1connect"
	"github.com/yangtao121/workos/internal/platform/config"
	"github.com/yangtao121/workos/internal/platform/ids"
)

// readSecret materializes the credential secret from operator-controlled
// input only: an explicit owner-only regular non-symlink file, or standard
// input. There is deliberately no --secret flag, no positional secret, and
// no environment fallback: shell history, argv listings, and process
// environments are not secret storage. Stdout never echoes the value, its
// length, or any fingerprint.
func readSecret(path string) ([]byte, error) {
	if path == "" {
		secret, err := io.ReadAll(os.Stdin)
		if err != nil {
			return nil, errors.New("read secret from stdin: standard input is unavailable")
		}
		return trimSecretNewline(secret), nil
	}
	if path == "-" {
		secret, err := io.ReadAll(os.Stdin)
		if err != nil {
			return nil, errors.New("read secret from stdin: standard input is unavailable")
		}
		return trimSecretNewline(secret), nil
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, errors.New("secret file is unavailable")
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, errors.New("secret file must be a regular file, not a symlink")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("secret file must be readable only by its owner (chmod 600)")
	}
	secret, err := os.ReadFile(path)
	if err != nil {
		return nil, errors.New("read secret file failed")
	}
	return trimSecretNewline(secret), nil
}

// trimSecretNewline removes exactly one trailing newline (or CRLF) that
// command pipelines conventionally add. Any other byte is preserved: secrets
// are never trimmed or normalized.
func trimSecretNewline(secret []byte) []byte {
	if len(secret) > 0 && secret[len(secret)-1] == '\n' {
		secret = secret[:len(secret)-1]
	}
	if len(secret) > 0 && secret[len(secret)-1] == '\r' {
		secret = secret[:len(secret)-1]
	}
	return secret
}

func credentialAdminClient(cfg config.Config) (credentialv1connect.CredentialAdminServiceClient, error) {
	socket := cfg.Credential.AdminSocketPath
	if socket == "" {
		return nil, errors.New("WORKOS_CREDENTIAL_ADMIN_SOCKET is not configured; the core credential admin socket path is required")
	}
	client := &http.Client{
		Timeout: 15 * time.Second,
		Transport: &http.Transport{
			Proxy: nil,
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				dialer := &net.Dialer{Timeout: 3 * time.Second}
				return dialer.DialContext(ctx, "unix", socket)
			},
		},
	}
	return credentialv1connect.NewCredentialAdminServiceClient(client, "http://unix"), nil
}

func printCredential(credential *credentialv1.CredentialMetadata) {
	status := credential.GetStatus().String()
	fmt.Printf("id: %s\n", credential.GetId())
	fmt.Printf("consumer: %s\n", credential.GetConsumerId())
	fmt.Printf("purpose: %s\n", credential.GetPurpose())
	if credential.GetLabel() != "" {
		fmt.Printf("label: %s\n", credential.GetLabel())
	}
	fmt.Printf("revision: %d\n", credential.GetRevision())
	fmt.Printf("status: %s\n", strings.TrimPrefix(status, "CREDENTIAL_STATUS_"))
	if credential.GetCreatedAt() != nil {
		fmt.Printf("created_at: %s\n", credential.GetCreatedAt().AsTime().UTC().Format(time.RFC3339Nano))
	}
	if credential.GetUpdatedAt() != nil {
		fmt.Printf("updated_at: %s\n", credential.GetUpdatedAt().AsTime().UTC().Format(time.RFC3339Nano))
	}
}

type credentialFlags struct {
	consumer         string
	purpose          string
	label            string
	credentialID     string
	expectedRevision int64
	idempotencyKey   string
	secretFile       string
}

func parseCredentialFlags(args []string, withSecret, withCredential bool) (*credentialFlags, error) {
	flags := &credentialFlags{}
	set := flag.NewFlagSet("workosctl credential", flag.ContinueOnError)
	set.SetOutput(io.Discard)
	set.StringVar(&flags.consumer, "consumer", "", "canonical consumer id, e.g. deepseek")
	set.StringVar(&flags.purpose, "purpose", "provider-api-key.v1", "canonical purpose")
	set.StringVar(&flags.label, "label", "", "optional human label (<= 80 code points)")
	set.StringVar(&flags.credentialID, "credential", "", "credential id (UUIDv7)")
	set.Int64Var(&flags.expectedRevision, "expected-revision", 0, "expected current revision (positive)")
	set.StringVar(&flags.idempotencyKey, "idempotency-key", "", "idempotency key (a UUIDv7 is generated and printed when omitted)")
	set.StringVar(&flags.secretFile, "secret-file", "", "owner-only secret file path, or '-' for stdin")
	if err := set.Parse(args); err != nil {
		return nil, errors.New("invalid credential flags")
	}
	if withSecret && flags.secretFile == "" {
		// Default to stdin: the operator pipes the secret; it never becomes
		// an argument or environment value.
		flags.secretFile = "-"
	}
	if withCredential && flags.credentialID == "" {
		return nil, errors.New("--credential is required")
	}
	if withCredential && flags.expectedRevision <= 0 {
		return nil, errors.New("--expected-revision is required (positive)")
	}
	return flags, nil
}

func runCredential(ctx context.Context, cfg config.Config, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: workosctl credential put|rotate|revoke|list")
	}
	admin, err := credentialAdminClient(cfg)
	if err != nil {
		return err
	}
	switch args[0] {
	case "put":
		flags, err := parseCredentialFlags(args[1:], true, false)
		if err != nil {
			return err
		}
		if flags.consumer == "" {
			return errors.New("--consumer is required")
		}
		secret, err := readSecret(flags.secretFile)
		if err != nil {
			return err
		}
		key := flags.idempotencyKey
		if key == "" {
			key = ids.UUIDv7{}.New()
			fmt.Printf("request key: %s\n", key)
		}
		response, err := admin.PutCredential(ctx, connect.NewRequest(&credentialv1.PutCredentialRequest{
			ConsumerId: flags.consumer, Purpose: flags.purpose, Label: flags.label,
			Secret: secret, IdempotencyKey: key,
		}))
		if err != nil {
			return fmt.Errorf("put credential: %w", err)
		}
		printCredential(response.Msg.GetCredential())
		return nil
	case "rotate":
		flags, err := parseCredentialFlags(args[1:], true, true)
		if err != nil {
			return err
		}
		secret, err := readSecret(flags.secretFile)
		if err != nil {
			return err
		}
		key := flags.idempotencyKey
		if key == "" {
			key = ids.UUIDv7{}.New()
			fmt.Printf("request key: %s\n", key)
		}
		response, err := admin.RotateCredential(ctx, connect.NewRequest(&credentialv1.RotateCredentialRequest{
			CredentialId: flags.credentialID, Secret: secret, Label: flags.label,
			ExpectedRevision: flags.expectedRevision, IdempotencyKey: key,
		}))
		if err != nil {
			return fmt.Errorf("rotate credential: %w", err)
		}
		printCredential(response.Msg.GetCredential())
		return nil
	case "revoke":
		flags, err := parseCredentialFlags(args[1:], false, true)
		if err != nil {
			return err
		}
		key := flags.idempotencyKey
		if key == "" {
			key = ids.UUIDv7{}.New()
			fmt.Printf("request key: %s\n", key)
		}
		response, err := admin.RevokeCredential(ctx, connect.NewRequest(&credentialv1.RevokeCredentialRequest{
			CredentialId: flags.credentialID, ExpectedRevision: flags.expectedRevision, IdempotencyKey: key,
		}))
		if err != nil {
			return fmt.Errorf("revoke credential: %w", err)
		}
		printCredential(response.Msg.GetCredential())
		return nil
	case "list":
		response, err := admin.ListCredentials(ctx, connect.NewRequest(&credentialv1.ListCredentialsRequest{}))
		if err != nil {
			return fmt.Errorf("list credentials: %w", err)
		}
		credentials := response.Msg.GetCredentials()
		if len(credentials) == 0 {
			fmt.Println("no credentials stored")
			return nil
		}
		for index, credential := range credentials {
			if index > 0 {
				fmt.Println()
			}
			printCredential(credential)
		}
		return nil
	default:
		return errors.New("usage: workosctl credential put|rotate|revoke|list")
	}
}
