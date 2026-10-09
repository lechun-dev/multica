package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
)

const DshProtocolVersion = 1
const dshShutdownGrace = 2 * time.Second

type dshBackend struct{ cfg Config }

func dshLaunchArgs() []string { return []string{"--profile", "acp"} }

// 2026-10-08 coder(lq): Official ACP option values are opaque; never decode or rebuild provider/model IDs.
func applyDshConfig(ctx context.Context, request acpRequestFn, sessionID string, state json.RawMessage, category, value string) (json.RawMessage, error) {
	if value == "" {
		return state, nil
	}
	var response struct {
		Options []struct {
			ID       string           `json:"id"`
			Category string           `json:"category"`
			Options  []acpSelectEntry `json:"options"`
		} `json:"configOptions"`
	}
	if err := json.Unmarshal(state, &response); err != nil {
		return nil, err
	}
	for _, option := range response.Options {
		if option.Category != category || option.ID == "" {
			continue
		}
		supported := false
		available := make([]string, 0)
		for _, choice := range flattenACPSelectChoices(option.Options) {
			available = append(available, choice.Value)
			if choice.Value == value {
				supported = true
			}
		}
		if !supported {
			// 2026-10-09 coder(lq): Show exact opaque selector values so operators can fix configuration without guessing aliases.
			return nil, fmt.Errorf("dsh does not advertise requested %s %q; advertised IDs: %q", category, value, available)
		}
		result, err := request(ctx, "session/set_config_option", map[string]any{"sessionId": sessionID, "configId": option.ID, "value": value})
		if err != nil {
			return nil, err
		}
		current, ok := acpConfigOptionCurrentValue(result, option.ID)
		if !ok || current != value {
			return nil, fmt.Errorf("dsh did not confirm requested %s %q", category, value)
		}
		return result, nil
	}
	return nil, fmt.Errorf("dsh advertises no %s selector", category)
}

func (b *dshBackend) Execute(ctx context.Context, prompt string, opts ExecOptions) (*Session, error) {
	path := b.cfg.ExecutablePath
	if path == "" {
		path = "dsh"
	}
	if _, err := exec.LookPath(path); err != nil {
		return nil, fmt.Errorf("dsh executable not found: %w", err)
	}
	// 2026-10-08 coder(lq): The ACP profile owns transport/bootstrap. Arbitrary profile flags could bypass the headless contract.
	if len(opts.CustomArgs) != 0 {
		return nil, fmt.Errorf("dsh ACP does not support custom arguments")
	}
	servers, err := buildACPMcpServers(opts.McpConfig, b.cfg.Logger)
	if err != nil {
		return nil, fmt.Errorf("dsh invalid mcp_config: %w", err)
	}
	env := replaceEnvValue(buildEnv(b.cfg.Env), "DSH_TELEMETRY_DISABLED", "1")
	args, cleanupConnection, err := prepareDshLaunch(env, opts.Cwd)
	if err != nil {
		return nil, err
	}
	connectionStarted := false
	defer func() {
		if !connectionStarted {
			cleanupConnection()
		}
	}()
	runCtx, cancel := runContext(ctx, opts.Timeout)
	cmd := b.cfg.commandAt(path).exec(runCtx, args...)
	hideAgentWindow(cmd)
	b.cfg.logAgentCommand(cmd, newAgentCommandLogArgs(args))
	cmd.Env = env
	if opts.Cwd != "" {
		cmd.Dir = opts.Cwd
	}
	// 2026-10-08 coder(lq): Give official ACP cancel/close a bounded flush window before killing the owned process tree.
	cmd.Cancel = func() error { return nil }
	cmd.WaitDelay = dshShutdownGrace
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	providerErr := newACPProviderErrorSniffer("dsh")
	cmd.Stderr = providerErr // 2026-10-08 coder(lq): Never copy credential-shaped provider diagnostics into the daemon log.
	if err := startOwnedProcessTree(cmd, b.cfg.Logger); err != nil {
		cancel()
		return nil, fmt.Errorf("start dsh: %w", err)
	}
	connectionStarted = true
	stream := newACPMessageStream(256)
	results := make(chan Result, 1)
	var delivering atomic.Bool
	var deliverable acpDeliverableTracker
	promptDone := make(chan hermesPromptResult, 1)
	client := &hermesClient{
		cfg: b.cfg, stdin: stdin, pending: make(map[int]*pendingRPC), pendingTools: make(map[string]*pendingToolCall),
		acceptNotification: func(string) bool { return delivering.Load() },
		onMessage: func(msg Message) {
			if delivering.Load() {
				deliverable.observe(msg)
				stream.send(msg)
			}
		},
		onPromptDone: func(result hermesPromptResult) {
			select {
			case promptDone <- result:
			default:
			}
		},
	}
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		scanner := newAgentStreamScanner(stdout)
		for scanner.Scan() {
			client.handleLine(strings.TrimSpace(scanner.Text()))
		}
		err := scanner.Err()
		if err == nil {
			err = io.EOF
		}
		client.closeAllPending(err)
	}()
	go func() {
		started := time.Now()
		result := Result{Status: "failed"}
		var sessionID string
		defer func() {
			delivering.Store(false)
			// 2026-10-08 coder(lq): Closing a session flushes persistence but does not delete it, so it can be resumed by another process.
			if sessionID != "" {
				closeCtx, closeCancel := context.WithTimeout(context.Background(), dshShutdownGrace)
				if runCtx.Err() != nil {
					data, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": "session/cancel", "params": map[string]any{"sessionId": sessionID}})
					_ = client.writeLine(append(data, '\n'))
				}
				_, _ = client.request(closeCtx, "session/close", map[string]any{"sessionId": sessionID})
				closeCancel()
			}
			_ = stdin.Close()
			signalProcessGroup(cmd, syscall.SIGKILL)
			_ = cmd.Wait()
			releaseProcessGroup(cmd)
			cancel()
			_ = stdout.Close()
			<-readerDone
			cleanupConnection()
			_, diagnostics := deliverable.result()
			result.Status, result.Error = promoteACPResultOnProviderError(result.Status, result.Error, diagnostics, providerErr)
			stream.close()
			result.Error = sanitizeAgentDiagnostic(result.Error)
			result.DurationMs = time.Since(started).Milliseconds()
			results <- result
			close(results)
		}()
		fail := func(stage string, err error) {
			result.Error = fmt.Sprintf("dsh %s failed: %v", stage, err)
			if runCtx.Err() == context.DeadlineExceeded {
				result.Status = "timeout"
			}
			if runCtx.Err() == context.Canceled {
				result.Status = "aborted"
			}
			if opts.ResumeSessionID != "" && isACPSessionNotFound(err) {
				result.SessionID = ""
				result.ResumeRejected = true
			}
		}
		init, err := client.request(runCtx, "initialize", map[string]any{
			"protocolVersion": DshProtocolVersion, "clientInfo": map[string]any{"name": "missionos", "version": "1"}, "clientCapabilities": map[string]any{},
		})
		if err != nil {
			fail("initialize", err)
			return
		}
		if validateDshACPInit(init) != nil {
			fail("initialize", fmt.Errorf("unsupported ACP protocol"))
			return
		}
		cwd := opts.Cwd
		if cwd == "" {
			cwd, err = os.Getwd()
			if err != nil {
				fail("cwd", err)
				return
			}
		}
		params := map[string]any{"cwd": cwd, "mcpServers": filterACPMcpServersByCapability(servers, extractACPMcpCapabilities(init), "dsh", b.cfg)}
		method := "session/new"
		if opts.ResumeSessionID != "" {
			method = "session/resume"
			params["sessionId"] = opts.ResumeSessionID
		}
		state, err := client.request(runCtx, method, params)
		if err != nil {
			if opts.ResumeSessionID != "" {
				result.Status, result.Error, result.ResumeRejected = classifyACPResumeFailure(runCtx, "dsh", method, err, opts.Timeout, b.cfg.Logger)
			} else {
				fail(method, err)
			}
			return
		}
		sessionID = extractACPSessionID(state)
		if opts.ResumeSessionID != "" {
			sessionID, _ = resolveResumedSessionID(opts.ResumeSessionID, state)
		}
		if sessionID == "" {
			fail(method, fmt.Errorf("no session ID returned"))
			return
		}
		if opts.ResumeSessionID != "" && sessionID != opts.ResumeSessionID {
			fail(method, fmt.Errorf("runtime returned a different session ID"))
			result.ResumeRejected = true
			return
		}
		client.sessionID = sessionID
		// 2026-10-08 coder(lq): A resumed transcript remains valid if model/effort setup fails; only fresh, unprompted IDs are withheld.
		if !setupFailureWithholdsSessionID(opts) {
			result.SessionID = sessionID
		}
		state, err = applyDshConfig(runCtx, client.request, sessionID, state, "model", opts.Model)
		if err != nil {
			fail("model selection", err)
			return
		}
		effectiveModel := opts.Model
		if effectiveModel == "" {
			for _, model := range parseACPConfigOptionModels(state) {
				if model.Default {
					effectiveModel = model.ID
				}
			}
		}
		_, err = applyDshConfig(runCtx, client.request, sessionID, state, "thought_level", opts.ThinkingLevel)
		if err != nil {
			fail("reasoning effort", err)
			return
		}
		result.SessionID = sessionID
		delivering.Store(true)
		stream.send(Message{Type: MessageStatus, Status: "running", SessionID: sessionID})
		text := prompt
		if opts.SystemPrompt != "" {
			text = opts.SystemPrompt + "\n\n---\n\n" + text
		}
		_, err = client.request(runCtx, "session/prompt", map[string]any{"sessionId": sessionID, "prompt": []map[string]any{{"type": "text", "text": text}}})
		if err != nil {
			fail("session/prompt", err)
			return
		}
		result.Status = "completed"
		select {
		case terminal := <-promptDone:
			client.mergeUsage(terminal.usage)
			switch terminal.stopReason {
			case "cancelled":
				result.Status = "aborted"
				result.Error = "dsh cancelled the prompt"
			case "end_turn", "max_tokens":
			default:
				result.Status = "failed"
				result.Error = "dsh returned an unsupported stop reason"
			}
		default:
			result.Status = "failed"
			result.Error = "dsh returned no terminal prompt result"
		}
		// 2026-10-08 coder(lq): Official ACP settles only after ordered updates have drained; no timing-based chunk guessing is needed.
		output, _ := deliverable.result()
		result.Output = output
		usage := client.accumulatedUsage()
		if acpUsagePresent(usage) {
			if effectiveModel == "" {
				effectiveModel = "unknown"
			}
			result.Usage = map[string]TokenUsage{effectiveModel: usage}
		}
	}()
	return &Session{Messages: stream.ch, Result: results}, nil
}

func validateDshACPInit(raw json.RawMessage) error {
	var result struct {
		ProtocolVersion int `json:"protocolVersion"`
	}
	if json.Unmarshal(raw, &result) != nil || result.ProtocolVersion != DshProtocolVersion {
		return fmt.Errorf("unsupported DSH ACP protocol")
	}
	return nil
}

func discoverDshModels(ctx context.Context, cmd Command) ([]Model, error) {
	args, cleanup, err := prepareDshLaunch(os.Environ(), "")
	if err != nil {
		return nil, err
	}
	defer cleanup()
	models, err := discoverACPModels(ctx, cmd, acpDiscoveryProvider{
		defaultBin: "dsh", clientName: "missionos", tmpdirPrefix: "missionos-dsh-models-", acpArgs: args,
		extraEnv: []string{"DSH_TELEMETRY_DISABLED=1"}, timeout: 8 * time.Second, strictErrors: true, closeSession: true,
		annotate: annotateACPThinkingForSessionModel, validateInit: validateDshACPInit,
	})
	if err == nil && len(models) == 0 {
		return nil, fmt.Errorf("DSH ACP returned no selectable models")
	}
	return models, err
}

// 2026-10-08 coder(lq): InspectDshACP validates official ACP without creating a session or making a model request.
func InspectDshACP(ctx context.Context, cmd Command) (string, error) {
	version := ""
	_, err := discoverACPModels(ctx, cmd, acpDiscoveryProvider{
		defaultBin: "dsh", clientName: "missionos", acpArgs: dshLaunchArgs(), initializeOnly: true,
		extraEnv: []string{"DSH_TELEMETRY_DISABLED=1"}, timeout: 8 * time.Second, strictErrors: true, validateInit: validateDshACPInit,
		inspectInit: func(raw json.RawMessage) {
			var result struct {
				AgentInfo struct {
					Version string `json:"version"`
				} `json:"agentInfo"`
			}
			if json.Unmarshal(raw, &result) == nil {
				version = result.AgentInfo.Version
			}
		},
	})
	return version, err
}
