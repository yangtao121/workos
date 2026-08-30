// Package transport adapts the device authentication application service to
// Connect handlers: the public pairing/session edge, the session-
// authenticated device management edge, and the private operator admin edge.
// It owns the __Host- session cookie shape, the sanitized error matrix, and
// the no-store response policy; it never logs or echoes secret material.
package transport

import (
	"context"
	"errors"
	"net/http"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	authv1 "github.com/yangtao121/workos/gen/go/workos/auth/v1"
	"github.com/yangtao121/workos/gen/go/workos/auth/v1/authv1connect"
	surfacev1 "github.com/yangtao121/workos/gen/go/workos/surface/v1"
	"github.com/yangtao121/workos/internal/gateway/auth/application"
	"github.com/yangtao121/workos/internal/gateway/auth/domain"
)

// SessionCookieName is the __Host- prefixed session cookie. The prefix is a
// browser-enforced contract: Secure, Path=/, no Domain attribute.
const SessionCookieName = "__Host-workos_session"

// SetSessionCookie writes the one-time raw session token. The attributes are
// fixed: Secure, HttpOnly, SameSite=Strict, Path=/, no Domain, absolute
// Max-Age/Expires matching the stored UTC expiry.
func SetSessionCookie(w http.ResponseWriter, token string, expires time.Time, now time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    token,
		Path:     "/",
		MaxAge:   int(expires.Sub(now).Round(time.Second) / time.Second),
		Expires:  expires,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
	})
}

// ClearSessionCookie invalidates the cookie with exactly the same attributes
// the setter used, plus an expired value.
func ClearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		Expires:  time.Unix(0, 0),
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteStrictMode,
	})
}

type identityContextKey struct{}

// writerContextKey carries the underlying http.ResponseWriter through the
// Connect handler chain so the completion responses can set the one-time
// session cookie. Only the Gateway edge injects it.
type writerContextKey struct{}

// WithResponseWriter injects the live ResponseWriter into the handler chain.
func WithResponseWriter(ctx context.Context, w http.ResponseWriter) context.Context {
	return context.WithValue(ctx, writerContextKey{}, w)
}

// connectHTTPWriter resolves the injected writer.
func connectHTTPWriter(ctx context.Context) (http.ResponseWriter, bool) {
	w, ok := ctx.Value(writerContextKey{}).(http.ResponseWriter)
	return w, ok && w != nil
}

// SessionContext carries the trusted per-request identity the Gateway gate
// derived from the validated session.
type SessionContext struct {
	Identity      domain.SessionIdentity
	SessionExpiry time.Time
}

// WithSessionContext injects the validated identity; only the Gateway gate
// may call it.
func WithSessionContext(ctx context.Context, value SessionContext) context.Context {
	return context.WithValue(ctx, identityContextKey{}, value)
}

// SessionFromContext resolves the trusted identity; its absence is the fixed
// "device session required" failure.
func SessionFromContext(ctx context.Context) (SessionContext, error) {
	value, ok := ctx.Value(identityContextKey{}).(SessionContext)
	if !ok {
		return SessionContext{}, connect.NewError(connect.CodeUnauthenticated, errors.New("device session required"))
	}
	return value, nil
}

// PairingHandler serves the public DevicePairingService: TLS-only pairing
// and session proofs. Origin/Host policy is enforced by the Gateway edge
// before the handler runs.
type PairingHandler struct {
	app *application.Service
	now func() time.Time
}

func NewPairingHandler(app *application.Service, now func() time.Time) *PairingHandler {
	return &PairingHandler{app: app, now: now}
}

func (h *PairingHandler) BeginPairing(ctx context.Context, req *connect.Request[authv1.BeginPairingRequest]) (*connect.Response[authv1.BeginPairingResponse], error) {
	result, err := h.app.BeginPairing(ctx, application.BeginPairingInput{
		PairingSecret: req.Msg.GetPairingSecret(),
		PublicKeySPKI: req.Msg.GetPublicKeySpki(),
		DeviceName:    req.Msg.GetDeviceName(),
		DeviceClass:   deviceClassFromProto(req.Msg.GetDeviceClass()),
	})
	if err != nil {
		return nil, verdict(err)
	}
	response := connect.NewResponse(&authv1.BeginPairingResponse{
		DeviceId: result.DeviceID,
		Challenge: &authv1.Challenge{
			ChallengeId: result.Challenge.ID,
			Nonce:       result.Challenge.Nonce,
			ExpiresAt:   timestamp(result.Challenge.ExpiresAt),
		},
		TicketId: result.TicketID,
	})
	noStore(response.Header())
	return response, nil
}

func (h *PairingHandler) CompletePairing(ctx context.Context, req *connect.Request[authv1.CompletePairingRequest]) (*connect.Response[authv1.CompletePairingResponse], error) {
	completion, err := h.app.CompletePairing(ctx, application.CompletePairingInput{
		DeviceID:      req.Msg.GetDeviceId(),
		ChallengeID:   req.Msg.GetChallengeId(),
		PublicKeySPKI: req.Msg.GetPublicKeySpki(),
		Signature:     req.Msg.GetSignature(),
	})
	if err != nil {
		return nil, verdict(err)
	}
	// The raw session token travels ONLY in the Set-Cookie header of this
	// response; the body never carries it.
	writer, ok := connectHTTPWriter(ctx)
	if !ok {
		return nil, verdict(domain.ErrAuthCorrupt)
	}
	SetSessionCookie(writer, completion.SessionToken, completion.SessionExpires, h.now())
	response := connect.NewResponse(&authv1.CompletePairingResponse{
		Device:           deviceInfo(completion.Device, false),
		SessionExpiresAt: timestamp(completion.SessionExpires),
	})
	noStore(response.Header())
	return response, nil
}

func (h *PairingHandler) BeginDeviceSession(ctx context.Context, req *connect.Request[authv1.BeginDeviceSessionRequest]) (*connect.Response[authv1.BeginDeviceSessionResponse], error) {
	challenge, err := h.app.BeginDeviceSession(ctx, req.Msg.GetDeviceId())
	if err != nil {
		return nil, verdict(err)
	}
	response := connect.NewResponse(&authv1.BeginDeviceSessionResponse{
		Challenge: &authv1.Challenge{
			ChallengeId: challenge.ID,
			Nonce:       challenge.Nonce,
			ExpiresAt:   timestamp(challenge.ExpiresAt),
		},
	})
	noStore(response.Header())
	return response, nil
}

func (h *PairingHandler) CompleteDeviceSession(ctx context.Context, req *connect.Request[authv1.CompleteDeviceSessionRequest]) (*connect.Response[authv1.CompleteDeviceSessionResponse], error) {
	completion, err := h.app.CompleteDeviceSession(ctx, application.CompleteSessionInput{
		DeviceID:    req.Msg.GetDeviceId(),
		ChallengeID: req.Msg.GetChallengeId(),
		Signature:   req.Msg.GetSignature(),
	})
	if err != nil {
		return nil, verdict(err)
	}
	writer, ok := connectHTTPWriter(ctx)
	if !ok {
		return nil, verdict(domain.ErrAuthCorrupt)
	}
	SetSessionCookie(writer, completion.SessionToken, completion.SessionExpires, h.now())
	response := connect.NewResponse(&authv1.CompleteDeviceSessionResponse{
		Device:           deviceInfo(completion.Device, false),
		SessionExpiresAt: timestamp(completion.SessionExpires),
	})
	noStore(response.Header())
	return response, nil
}

// DeviceHandler serves the session-authenticated DeviceService. Owner scope
// always derives from the injected session context.
type DeviceHandler struct {
	app *application.Service
	now func() time.Time
}

func NewDeviceHandler(app *application.Service, now func() time.Time) *DeviceHandler {
	return &DeviceHandler{app: app, now: now}
}

func (h *DeviceHandler) GetCurrentDevice(ctx context.Context, _ *connect.Request[authv1.GetCurrentDeviceRequest]) (*connect.Response[authv1.GetCurrentDeviceResponse], error) {
	session, err := SessionFromContext(ctx)
	if err != nil {
		return nil, err
	}
	device, expires, err := h.app.CurrentDevice(ctx, session.Identity, session.SessionExpiry)
	if err != nil {
		return nil, verdict(err)
	}
	response := connect.NewResponse(&authv1.GetCurrentDeviceResponse{
		Device:           deviceInfo(device, true),
		SessionExpiresAt: timestamp(expires),
	})
	noStore(response.Header())
	return response, nil
}

func (h *DeviceHandler) ListDevices(ctx context.Context, req *connect.Request[authv1.ListDevicesRequest]) (*connect.Response[authv1.ListDevicesResponse], error) {
	session, err := SessionFromContext(ctx)
	if err != nil {
		return nil, err
	}
	devices, next, err := h.app.ListDevices(ctx, session.Identity, int(req.Msg.GetPageSize()), req.Msg.GetPageToken())
	if err != nil {
		return nil, verdict(err)
	}
	infos := make([]*authv1.DeviceInfo, 0, len(devices))
	for _, device := range devices {
		infos = append(infos, deviceInfo(device, device.ID == session.Identity.DeviceID))
	}
	response := connect.NewResponse(&authv1.ListDevicesResponse{Devices: infos, NextPageToken: next})
	noStore(response.Header())
	return response, nil
}

func (h *DeviceHandler) RotatePairingTicket(ctx context.Context, _ *connect.Request[authv1.RotatePairingTicketRequest]) (*connect.Response[authv1.RotatePairingTicketResponse], error) {
	session, err := SessionFromContext(ctx)
	if err != nil {
		return nil, err
	}
	info, err := h.app.RotatePairingTicket(ctx, session.Identity.OwnerID)
	if err != nil {
		return nil, verdict(err)
	}
	response := connect.NewResponse(&authv1.RotatePairingTicketResponse{Ticket: ticketInfo(info)})
	noStore(response.Header())
	return response, nil
}

func (h *DeviceHandler) RevokeDevice(ctx context.Context, req *connect.Request[authv1.RevokeDeviceRequest]) (*connect.Response[authv1.RevokeDeviceResponse], error) {
	session, err := SessionFromContext(ctx)
	if err != nil {
		return nil, err
	}
	device, replayed, err := h.app.RevokeDevice(ctx, session.Identity, application.RevokeDeviceInput{
		DeviceID:         req.Msg.GetDeviceId(),
		IdempotencyKey:   req.Msg.GetIdempotencyKey(),
		ExpectedRevision: req.Msg.GetExpectedRevision(),
	})
	if err != nil {
		return nil, verdict(err)
	}
	if device.ID == session.Identity.DeviceID {
		// Revoking the current device ends this very session: the cookie is
		// cleared with the same attributes the setter used.
		if writer, ok := connectHTTPWriter(ctx); ok {
			ClearSessionCookie(writer)
		}
	}
	response := connect.NewResponse(&authv1.RevokeDeviceResponse{
		Device:   deviceInfo(device, device.ID == session.Identity.DeviceID),
		Replayed: replayed,
	})
	noStore(response.Header())
	return response, nil
}

func (h *DeviceHandler) Logout(ctx context.Context, _ *connect.Request[authv1.LogoutRequest]) (*connect.Response[authv1.LogoutResponse], error) {
	session, err := SessionFromContext(ctx)
	if err != nil {
		return nil, err
	}
	if err := h.app.Logout(ctx, session.Identity); err != nil {
		return nil, verdict(err)
	}
	now := h.now()
	if writer, ok := connectHTTPWriter(ctx); ok {
		ClearSessionCookie(writer)
	}
	response := connect.NewResponse(&authv1.LogoutResponse{SessionRevokedAt: timestamp(now)})
	noStore(response.Header())
	return response, nil
}

// AdminHandler serves the private DeviceAuthAdminService. It is registered
// exclusively on the Gateway-owned admin Unix socket mux.
type AdminHandler struct {
	app     *application.Service
	ownerID string
}

func NewAdminHandler(app *application.Service, ownerID string) *AdminHandler {
	return &AdminHandler{app: app, ownerID: ownerID}
}

func (h *AdminHandler) RotatePairingTicket(ctx context.Context, _ *connect.Request[authv1.DeviceAuthAdminServiceRotatePairingTicketRequest]) (*connect.Response[authv1.DeviceAuthAdminServiceRotatePairingTicketResponse], error) {
	info, err := h.app.RotatePairingTicket(ctx, h.ownerID)
	if err != nil {
		return nil, verdict(err)
	}
	response := connect.NewResponse(&authv1.DeviceAuthAdminServiceRotatePairingTicketResponse{Ticket: ticketInfo(info)})
	noStore(response.Header())
	return response, nil
}

// ticketInfo maps the application pairing info onto the wire contract.
func ticketInfo(info domain.PairingInfo) *authv1.PairingTicket {
	return &authv1.PairingTicket{
		TicketId:       info.TicketID,
		PairingUrl:     info.PairingURL,
		PublicOrigin:   info.PublicOrigin,
		TlsFingerprint: info.TLSFingerprint,
		ExpiresAt:      timestamp(info.ExpiresAt),
	}
}

func deviceInfo(device domain.Device, isCurrent bool) *authv1.DeviceInfo {
	info := &authv1.DeviceInfo{
		DeviceId:  device.ID,
		Name:      device.Name,
		Revision:  device.Revision,
		CreatedAt: timestamp(device.CreatedAt),
		IsCurrent: isCurrent,
	}
	info.DeviceClass = deviceClassToProto(device.Class)
	info.LastAuthenticatedAt = timestamp(device.LastAuthenticatedAt)
	if device.RevokedAt != nil {
		info.RevokedAt = timestamp(*device.RevokedAt)
	}
	return info
}

func timestamp(value time.Time) *timestamppb.Timestamp {
	// Keep nil semantics for zero times instead of a fake epoch.
	if value.IsZero() {
		return nil
	}
	return timestamppb.New(value.UTC())
}

func noStore(header http.Header) {
	header.Set("Cache-Control", "no-store")
}

// verdict maps the sanitized domain verdicts onto the fixed public error
// matrix; messages are constants and never carry request facts.
func verdict(err error) error {
	if err == nil {
		return nil
	}
	var target *connect.Error
	if errors.As(err, &target) {
		return err
	}
	switch {
	case errors.Is(err, domain.ErrInvalidRequest):
		return connect.NewError(connect.CodeInvalidArgument, errors.New("invalid device authentication request"))
	case errors.Is(err, domain.ErrAuthenticationFailed):
		return connect.NewError(connect.CodeUnauthenticated, errors.New("device authentication failed"))
	case errors.Is(err, domain.ErrDeviceNotFound):
		return connect.NewError(connect.CodeNotFound, errors.New("device not found"))
	case errors.Is(err, domain.ErrConflict):
		return connect.NewError(connect.CodeAborted, errors.New("device changed / request conflict"))
	case errors.Is(err, domain.ErrRateLimited):
		return connect.NewError(connect.CodeResourceExhausted, errors.New("too many attempts, retry later"))
	case errors.Is(err, domain.ErrStoreUnavailable):
		return connect.NewError(connect.CodeUnavailable, errors.New("gateway auth unavailable"))
	case errors.Is(err, domain.ErrAuthCorrupt):
		return connect.NewError(connect.CodeInternal, errors.New("device authentication failed"))
	default:
		return connect.NewError(connect.CodeInternal, errors.New("device authentication failed"))
	}
}

// deviceClassFromProto maps the wire enum onto the domain grammar; the
// unspecified value falls through to the domain's invalid-request verdict.
func deviceClassFromProto(class surfacev1.DeviceClass) string {
	switch class {
	case surfacev1.DeviceClass_DEVICE_CLASS_DESKTOP:
		return string(domain.DeviceClassDesktop)
	case surfacev1.DeviceClass_DEVICE_CLASS_TABLET:
		return string(domain.DeviceClassTablet)
	case surfacev1.DeviceClass_DEVICE_CLASS_FOLDABLE:
		return string(domain.DeviceClassFoldable)
	case surfacev1.DeviceClass_DEVICE_CLASS_PHONE:
		return string(domain.DeviceClassPhone)
	default:
		return ""
	}
}

// deviceClassToProto maps the stored domain grammar back onto the wire
// enum; unknown stored values collapse to UNSPECIFIED (corruption is
// handled by validation before any response is built).
func deviceClassToProto(class domain.DeviceClass) surfacev1.DeviceClass {
	switch class {
	case domain.DeviceClassDesktop:
		return surfacev1.DeviceClass_DEVICE_CLASS_DESKTOP
	case domain.DeviceClassTablet:
		return surfacev1.DeviceClass_DEVICE_CLASS_TABLET
	case domain.DeviceClassFoldable:
		return surfacev1.DeviceClass_DEVICE_CLASS_FOLDABLE
	case domain.DeviceClassPhone:
		return surfacev1.DeviceClass_DEVICE_CLASS_PHONE
	default:
		return surfacev1.DeviceClass_DEVICE_CLASS_UNSPECIFIED
	}
}

// Compile-time interface pins: the handlers implement the generated Connect
// contracts, and the admin service is wired only onto the Unix socket mux.
var (
	_ authv1connect.DevicePairingServiceHandler   = (*PairingHandler)(nil)
	_ authv1connect.DeviceServiceHandler          = (*DeviceHandler)(nil)
	_ authv1connect.DeviceAuthAdminServiceHandler = (*AdminHandler)(nil)
)
