package agent

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/datadog-labs/bits-cli/internal/assistant"
	"github.com/datadog-labs/bits-cli/internal/imageinput"
)

// prepareImageContext loads image file URLs as data URLs, drops duplicates and
// enforces MaxImages. Recalled images are resent as is, without resize notice.
func prepareImageContext(ctx context.Context, input *assistant.AssistantContext) (*assistant.AssistantContext, []imageinput.Image, error) {
	if input == nil || !slices.ContainsFunc(input.Entities, isImage) {
		return input, nil, nil
	}
	prepared := &assistant.AssistantContext{Resources: input.Resources}
	var notices []imageinput.Image
	seen := make(map[string]bool)
	for _, entity := range input.Entities {
		if !isImage(entity) {
			prepared.Entities = append(prepared.Entities, entity)
			continue
		}
		var img imageinput.Image
		path, ok, err := imageinput.FilePath(entity.ID)
		if err != nil {
			return nil, nil, err
		}
		if ok {
			if img, err = imageinput.Load(ctx, path, imageinput.DefaultLimits); err != nil {
				return nil, nil, err
			}
			entity = assistant.ImageEntity(string(img.MediaType), img.Data, entity.Label)
		}
		if seen[entity.ID] {
			continue
		}
		seen[entity.ID] = true
		if len(notices) == imageinput.MaxImages {
			return nil, nil, imageinput.ErrTooManyImages
		}
		prepared.Entities = append(prepared.Entities, entity)
		notices = append(notices, img)
	}
	return prepared, notices, nil
}

func isImage(entity assistant.ContextEntity) bool {
	return entity.Type == assistant.EntityImageURL
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
