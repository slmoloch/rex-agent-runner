// Package dispatchcmd implements `rex dispatch <session> <message>`. It POSTs
// to the local daemon's /job endpoint so a fresh Claude turn runs in the
// given session.
package dispatchcmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/slmoloch/rex-agent-runner/internal/config"
)

const Usage = `Usage: rex dispatch <session> <message>

Dispatch a prompt to a session via the bot.

Sessions:
  main             The user-facing Telegram session
  new              Ephemeral session (discarded after)
  <session_id>     A raw Claude session ID (for agent-to-agent callbacks)`

func Run(ctx context.Context, cfg *config.Config, args []string) error {
	if len(args) < 2 {
		return fmt.Errorf("%s", Usage)
	}
	session := args[0]
	message := strings.Join(args[1:], " ")

	port := cfg.JobPort
	if port == 0 {
		port = 9821
	}

	payload := map[string]any{
		"prompt":   message,
		"job_name": "dispatch:" + session,
		"session":  session,
	}
	if caller := os.Getenv("REX_SESSION_ID"); caller != "" {
		payload["caller_session"] = caller
	}
	body, _ := json.Marshal(payload)

	cctx, cancel := context.WithTimeout(ctx, 620*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodPost,
		fmt.Sprintf("http://127.0.0.1:%d/job", port), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("could not connect to bot — is it running? (%w)", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	var out struct {
		Status   string `json:"status"`
		Response string `json:"response,omitempty"`
	}
	_ = json.Unmarshal(data, &out)
	fmt.Printf("Dispatched to session '%s': %s\n", session, out.Status)
	return nil
}
