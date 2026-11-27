package provider

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/eclipsesource/homeshell/daemon/internal/domain"
)

// ImageAttacher is a session provider that can attach an image to a session:
// put it where the session's agent can read it and hand it to the agent,
// without submitting the agent's input. The sessions it can do that for
// declare domain.SessionCapabilities.AttachImage.
type ImageAttacher interface {
	// AttachImage returns the image's path as the session's agent sees it. An
	// error's message is shown to the user as it is.
	AttachImage(ctx context.Context, ref SessionRef, image Image) (string, error)
}

// Image is an image to attach: its bytes and their media type.
type Image struct {
	// MediaType is image/png or image/jpeg.
	MediaType string
	Data      []byte
}

// MaxImageBytes bounds an image to attach.
const MaxImageBytes = 10 << 20

// imageSignatures are the media types an image can be attached as, each with
// the magic bytes its data starts with.
var imageSignatures = map[string][]byte{
	"image/png":  []byte("\x89PNG\r\n\x1a\n"),
	"image/jpeg": []byte("\xff\xd8\xff"),
}

var (
	// ErrInvalidImage is returned for an image that is not a PNG or JPEG, or
	// whose bytes are not what its media type says.
	ErrInvalidImage = errors.New("only PNG or JPEG images can be attached")
	// ErrSessionNotAttachable is returned when a session that can take images
	// is in a state that must not get one: waiting, where the image could pick
	// a menu option, or starting, where the agent may drop it.
	ErrSessionNotAttachable = errors.New("session is not idle or running; attach the image once it is")
)

// ValidateImage checks that image is a PNG or JPEG whose bytes match its media
// type.
func ValidateImage(image Image) error {
	signature, ok := imageSignatures[image.MediaType]
	if !ok {
		return fmt.Errorf("%w, not %q", ErrInvalidImage, image.MediaType)
	}
	if !bytes.HasPrefix(image.Data, signature) {
		return fmt.Errorf("%w: the data is not a %s", ErrInvalidImage, strings.TrimPrefix(image.MediaType, "image/"))
	}
	return nil
}

// CheckAttachable reports whether an image can be attached to session now: it
// must declare AttachImage and be idle or running.
func CheckAttachable(session domain.Session) error {
	if IsStartingPlaceholder(session) {
		return ErrSessionStarting
	}
	if session.Capabilities == nil || !session.Capabilities.AttachImage {
		return ErrCapabilityUnsupported
	}
	switch session.State {
	case domain.SessionIdle, domain.SessionRunning:
		return nil
	case domain.SessionStopped:
		return ErrSessionStopped
	default:
		return ErrSessionNotAttachable
	}
}

// AttachImage attaches an image to a session via its owning ImageAttacher and
// returns the image's path as the session's agent sees it. It does not
// re-project; the provider's watcher observes the effect.
func (s *Service) AttachImage(ctx context.Context, ref SessionRef, image Image) (string, error) {
	if err := ValidateImage(image); err != nil {
		return "", err
	}
	workstream, err := s.store.Workstream(ref.ProjectID, ref.WorkstreamID)
	if err != nil {
		return "", ErrWorkstreamNotFound
	}
	index := slices.IndexFunc(workstream.Sessions, func(session domain.Session) bool { return session.ID == ref.SessionID })
	if index < 0 {
		return "", ErrSessionNotFound
	}
	if err := CheckAttachable(workstream.Sessions[index]); err != nil {
		return "", err
	}
	sessionProvider, err := s.sessionProvider(workstream, ref.SessionID)
	if err != nil {
		return "", err
	}
	attacher, ok := sessionProvider.(ImageAttacher)
	if !ok {
		return "", ErrCapabilityUnsupported
	}
	return attacher.AttachImage(ctx, ref, image)
}
