package acp

import (
	"encoding/base64"
	"errors"
	"fmt"
	"path"
	"strings"

	acp "github.com/coder/acp-go-sdk"
	"github.com/voocel/litellm"
)

// promptBlocks turns a prompt into the model's input. A link to a file
// stays in the text where the user put it, for the agent to read; the text
// of an embedded resource follows the prompt in a <context> tag.
func promptBlocks(blocks []acp.ContentBlock) ([]litellm.Block, error) {
	var out, context []litellm.Block
	for _, b := range blocks {
		switch {
		case b.Text != nil:
			out = append(out, litellm.Text(b.Text.Text))
		case b.Image != nil:
			img, err := image(b.Image.Data, b.Image.MimeType)
			if err != nil {
				return nil, err
			}
			out = append(out, img)
		case b.ResourceLink != nil:
			out = append(out, litellm.Text(link(b.ResourceLink.Uri, b.ResourceLink.Name)))
		case b.Resource != nil:
			switch r := b.Resource.Resource; {
			case r.TextResourceContents != nil:
				out = append(out, litellm.Text(link(r.TextResourceContents.Uri, "")))
				context = append(context, litellm.Text("\n<context ref=\""+r.TextResourceContents.Uri+"\">\n"+r.TextResourceContents.Text+"\n</context>"))
			case r.BlobResourceContents != nil:
				mime := ""
				if r.BlobResourceContents.MimeType != nil {
					mime = *r.BlobResourceContents.MimeType
				}
				if !strings.HasPrefix(mime, "image/") {
					return nil, fmt.Errorf("acp: codebot takes no %q content", mime)
				}
				img, err := image(r.BlobResourceContents.Blob, mime)
				if err != nil {
					return nil, err
				}
				out = append(out, img)
			}
		case b.Audio != nil:
			return nil, errors.New("acp: codebot takes no audio")
		}
	}
	out = append(out, context...)
	if len(out) == 0 {
		return nil, errors.New("acp: the prompt is empty")
	}
	return out, nil
}

func image(data, mime string) (litellm.Block, error) {
	raw, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return nil, fmt.Errorf("acp: image: %w", err)
	}
	return litellm.ImageBlock{Data: raw, MIME: mime}, nil
}

// link names a file the way a mention reads in the prompt, "[@main.go](uri)".
func link(uri, name string) string {
	if !strings.HasPrefix(uri, "file://") && !strings.HasPrefix(uri, "zed://") {
		return uri
	}
	if name == "" {
		name = path.Base(uri)
	}
	return "[@" + name + "](" + uri + ")"
}
