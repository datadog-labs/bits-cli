package agent

import (
	"fmt"
	"strings"

	"github.com/datadog-labs/bits-cli/internal/assistant"
	"github.com/datadog-labs/bits-cli/internal/imageinput"
)

// validateImages rejects what the backend would silently drop.
func validateImages(images []imageinput.Image) error {
	for _, img := range images {
		if mediaType, ok := imageinput.Sniff(img.Data); !ok || mediaType != img.MediaType {
			return fmt.Errorf("%w: %q", ErrInvalidImage, img.Name)
		}
	}
	return nil
}

// messageContext appends images to a copy of the turn context.
func messageContext(turnContext *assistant.AssistantContext, images []imageinput.Image) *assistant.AssistantContext {
	if len(images) == 0 {
		return turnContext
	}
	merged := &assistant.AssistantContext{}
	if turnContext != nil {
		merged.Entities = append(merged.Entities, turnContext.Entities...)
		merged.Resources = turnContext.Resources
	}
	for _, img := range images {
		merged.Entities = append(merged.Entities, assistant.ImageEntity(string(img.MediaType), img.Data, img.Name))
	}
	return merged
}

// resizeNotice tells the model when it sees fewer pixels than the user has,
// so answers about sizes or positions can account for it.
func resizeNotice(images []imageinput.Image) string {
	var notice strings.Builder
	for i, img := range images {
		if img.Width == img.SourceWidth && img.Height == img.SourceHeight {
			continue
		}
		fmt.Fprintf(&notice, "Image %d of %d attached to this message was resized from %dx%d to %dx%d pixels.\n",
			i+1, len(images), img.SourceWidth, img.SourceHeight, img.Width, img.Height)
	}
	if notice.Len() == 0 {
		return ""
	}
	return "<image_resize_notice>\n" + notice.String() + "</image_resize_notice>"
}
