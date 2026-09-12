package service

import (
	"errors"

	"github.com/ttobak/backend/internal/model"
)

const (
	IndexModeManualOnly = "manual-only"
	IndexModeAll        = "all"
)

var ErrIndexMode = errors.New("invalid indexing rollout mode")

func ValidIndexingMode(mode string) bool {
	return mode == IndexModeManualOnly || mode == IndexModeAll
}

func (s *IndexingService) SetIndexingMode(mode string) error {
	if !ValidIndexingMode(mode) {
		return ErrIndexMode
	}
	s.mode = mode
	return nil
}

func (s *IndexingService) checkIndexingMode(control *model.IndexControl) error {
	if !ValidIndexingMode(s.mode) || (control.Mode != "" && !ValidIndexingMode(control.Mode)) {
		return ErrIndexMode
	}
	if s.mode == IndexModeManualOnly {
		if control.Mode == IndexModeAll {
			return ErrIndexMode
		}
		for _, member := range control.Batch {
			if !member.Resource.IsKnowledgeSource() {
				return ErrIndexMode
			}
		}
	}
	return nil
}

func (s *IndexingService) indexesResource(key model.IndexResource) bool {
	return s.mode == IndexModeAll || (s.mode == IndexModeManualOnly && key.IsKnowledgeSource())
}
