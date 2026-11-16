// Package tgclient talks to Telegram over MTProto (like app/controllers/utils/openbravo, an external integration).
package tgclient

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"math/rand/v2"
	"os"
	"strings"

	"github.com/gotd/td/session"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/telegram/query/dialogs"
	"github.com/gotd/td/telegram/uploader"
	"github.com/gotd/td/tg"
)

// upload.getFile rules: limit must divide 1 MiB and a request must not cross a 1 MiB boundary.
// Fetching whole 1 MiB-aligned chunks satisfies both.
const chunkSize = 1 << 20

type Client struct {
	client  *telegram.Client
	api     *tg.Client
	channel *tg.InputChannel
}

func New(apiID int, apiHash, sessionPath string) *Client {
	return &Client{client: telegram.NewClient(apiID, apiHash, telegram.Options{
		SessionStorage: &session.FileStorage{Path: sessionPath},
	})}
}

// Run logs in (asking for the code in the terminal on first run), finds the storage channel,
// then calls f while the connection stays up.
func (c *Client) Run(ctx context.Context, phone string, channelID int64, f func(context.Context) error) error {
	return c.client.Run(ctx, func(ctx context.Context) error {
		flow := auth.NewFlow(terminalAuth{phone: phone}, auth.SendCodeOptions{})
		if err := c.client.Auth().IfNecessary(ctx, flow); err != nil {
			return fmt.Errorf("telegram login: %w", err)
		}
		c.api = c.client.API()
		ch, err := c.findChannel(ctx, channelID)
		if err != nil {
			return err
		}
		c.channel = ch
		log.Printf("telegram ready, storage channel %d", channelID)
		return f(ctx)
	})
}

// findChannel walks the account's dialogs, because a user account needs the channel's access hash.
func (c *Client) findChannel(ctx context.Context, channelID int64) (*tg.InputChannel, error) {
	// Bot API style ids look like -100xxxxxxxxxx; MTProto uses the bare xxxxxxxxxx.
	id := channelID
	if id < -1_000_000_000_000 {
		id = -id - 1_000_000_000_000
	} else if id < 0 {
		id = -id
	}
	var found *tg.InputChannel
	errFound := errors.New("found")
	err := dialogs.NewQueryBuilder(c.api).GetDialogs().BatchSize(100).ForEach(ctx, func(_ context.Context, e dialogs.Elem) error {
		if p, ok := e.Peer.(*tg.InputPeerChannel); ok && p.ChannelID == id {
			found = &tg.InputChannel{ChannelID: p.ChannelID, AccessHash: p.AccessHash}
			return errFound
		}
		return nil
	})
	if found != nil {
		return found, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list dialogs: %w", err)
	}
	return nil, fmt.Errorf("channel %d not found in this account's chats (TELEGRAM_CHANNEL_ID wrong, or account not a member)", channelID)
}

type ProgressFunc func(sent int64)

func (f ProgressFunc) Chunk(_ context.Context, s uploader.ProgressState) error {
	f(s.Uploaded)
	return nil
}

// Upload sends r as a document to the storage channel and returns the message id.
func (c *Client) Upload(ctx context.Context, r io.Reader, name string, size int64, mimeType string, onProgress ProgressFunc) (int, error) {
	file, err := uploader.NewUploader(c.api).WithThreads(8).WithProgress(onProgress).
		Upload(ctx, uploader.NewUpload(name, r, size))
	if err != nil {
		return 0, fmt.Errorf("upload parts: %w", err)
	}
	upd, err := c.api.MessagesSendMedia(ctx, &tg.MessagesSendMediaRequest{
		Peer: &tg.InputPeerChannel{ChannelID: c.channel.ChannelID, AccessHash: c.channel.AccessHash},
		Media: &tg.InputMediaUploadedDocument{
			File:       file,
			MimeType:   mimeType,
			ForceFile:  true, // keep bytes as-is, no video/photo conversion
			Attributes: []tg.DocumentAttributeClass{&tg.DocumentAttributeFilename{FileName: name}},
		},
		RandomID: rand.Int64(),
	})
	if err != nil {
		return 0, fmt.Errorf("send media: %w", err)
	}
	if u, ok := upd.(*tg.Updates); ok {
		for _, x := range u.Updates {
			if m, ok := x.(*tg.UpdateNewChannelMessage); ok {
				return m.Message.GetID(), nil
			}
		}
	}
	return 0, errors.New("send media: no message id in response")
}

// Open returns a seekable reader over the document in message msgID.
// The message is fetched fresh each time so the file reference is never stale.
// parallel is how many 1 MiB chunks to fetch ahead: 8 for streaming, 1 to read just the start.
func (c *Client) Open(ctx context.Context, msgID int, parallel int) (io.ReadSeeker, error) {
	res, err := c.api.ChannelsGetMessages(ctx, &tg.ChannelsGetMessagesRequest{
		Channel: c.channel,
		ID:      []tg.InputMessageClass{&tg.InputMessageID{ID: msgID}},
	})
	if err != nil {
		return nil, fmt.Errorf("get message: %w", err)
	}
	var doc *tg.Document
	if cm, ok := res.(*tg.MessagesChannelMessages); ok && len(cm.Messages) > 0 {
		if m, ok := cm.Messages[0].(*tg.Message); ok {
			if md, ok := m.Media.(*tg.MessageMediaDocument); ok {
				doc, _ = md.Document.(*tg.Document)
			}
		}
	}
	if doc == nil {
		return nil, fmt.Errorf("message %d has no document (deleted from channel?)", msgID)
	}
	loc := &tg.InputDocumentFileLocation{ID: doc.ID, AccessHash: doc.AccessHash, FileReference: doc.FileReference}
	return &chunkReader{size: doc.Size, parallel: parallel, fetch: func(off int64) ([]byte, error) {
		r, err := c.api.UploadGetFile(ctx, &tg.UploadGetFileRequest{Location: loc, Offset: off, Limit: chunkSize})
		if err != nil {
			return nil, fmt.Errorf("get file chunk: %w", err)
		}
		f, ok := r.(*tg.UploadFile)
		if !ok {
			return nil, fmt.Errorf("get file chunk: unexpected %T", r)
		}
		return f.Bytes, nil
	}}, nil
}

func (c *Client) Delete(ctx context.Context, msgID int) error {
	_, err := c.api.ChannelsDeleteMessages(ctx, &tg.ChannelsDeleteMessagesRequest{Channel: c.channel, ID: []int{msgID}})
	return err
}

type terminalAuth struct{ phone string }

var stdin = bufio.NewReader(os.Stdin)

func prompt(msg string) (string, error) {
	fmt.Print(msg)
	s, err := stdin.ReadString('\n')
	return strings.TrimSpace(s), err
}

func (a terminalAuth) Phone(context.Context) (string, error) { return a.phone, nil }
func (a terminalAuth) Password(context.Context) (string, error) {
	return prompt("Telegram 2FA password: ")
}
func (a terminalAuth) Code(context.Context, *tg.AuthSentCode) (string, error) {
	return prompt("Telegram login code: ")
}
func (a terminalAuth) AcceptTermsOfService(context.Context, tg.HelpTermsOfService) error {
	return errors.New("this phone has no Telegram account; sign up in the Telegram app first")
}
func (a terminalAuth) SignUp(context.Context) (auth.UserInfo, error) {
	return auth.UserInfo{}, errors.New("sign up not supported")
}
