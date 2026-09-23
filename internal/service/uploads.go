package service

import (
	"errors"
	"github.com/newo-ether/filo/internal/uploads"
)

func (s *Server) attachments(text string, value any) (string, error) {
	values, ok := value.([]any)
	if !ok || len(values) > 16 {
		return "", errors.New("Invalid attachment identities")
	}
	if len(values) == 0 {
		return text, nil
	}
	if s.uploads == nil {
		return "", errors.New("Attachment storage is unavailable")
	}
	ids := make([]string, 0, len(values))
	for _, value := range values {
		id, ok := value.(string)
		if !ok {
			return "", uploads.ErrInvalid
		}
		ids = append(ids, id)
	}
	files, err := s.uploads.Resolve(ids)
	if err != nil {
		return "", err
	}
	result := uploads.References(text, files)
	if len(result) > maxBodyBytes {
		return "", errors.New("Message and attachment references exceed 64 KiB")
	}
	return result, nil
}
