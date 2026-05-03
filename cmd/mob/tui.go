package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/cdotlock/mob-sandbox/pkg/config"
	"github.com/cdotlock/mob-sandbox/pkg/daytona"
	"github.com/cdotlock/mob-sandbox/pkg/power"
	"github.com/cdotlock/mob-sandbox/pkg/remote"
	"github.com/cdotlock/mob-sandbox/pkg/ui"
	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

type tuiMode int

const (
	modeDashboard tuiMode = iota
	modePrompt
	modeBusy
)

type promptKind int

const (
	promptNone promptKind = iota
	promptForwardPort
	promptURLPort
	promptExposePort
	promptExposeName
	promptDelete
	promptStopTunnel
	promptPowerAction
	promptPowerConfirm
)

type sandboxItem struct {
	id    string
	state string
}

func (i sandboxItem) Title() string       { return fmt.Sprintf("%-12s %s", shortID(i.id), i.state) }
func (i sandboxItem) Description() string { return i.id }
func (i sandboxItem) FilterValue() string { return i.id + " " + i.state }

type activeForward struct {
	ID         int
	SandboxID  string
	RemotePort int
	LocalPort  int
	cancel     context.CancelFunc
}

type promptState struct {
	kind      promptKind
	title     string
	help      string
	sandboxID string
	port      int
	action    string
}

type tuiModel struct {
	cfg        *config.ClientConfig
	client     *daytona.Client
	list       list.Model
	spinner    spinner.Model
	input      textinput.Model
	mode       tuiMode
	prompt     promptState
	busyLabel  string
	status     string
	statusKind string
	forwards   []activeForward
	nextTunnel int
	width      int
	height     int
	showHelp   bool
}

type sandboxesLoadedMsg struct {
	sandboxes []daytona.Sandbox
	err       error
}

type sandboxCreatedMsg struct {
	id       string
	duration time.Duration
	err      error
}

type sshPreparedMsg struct {
	id     string
	token  string
	claude bool
	err    error
}

type terminalFinishedMsg struct {
	label string
	err   error
}

type forwardStartedMsg struct {
	forward activeForward
	err     error
}

type exposeCreatedMsg struct {
	response string
	err      error
}

type sandboxDeletedMsg struct {
	id  string
	err error
}

type powerDoneMsg struct {
	action string
	output string
	err    error
}

type browserOpenedMsg struct {
	err error
}

type terminalFunc struct {
	run func() error
}

func (f terminalFunc) Run() error          { return f.run() }
func (f terminalFunc) SetStdin(io.Reader)  {}
func (f terminalFunc) SetStdout(io.Writer) {}
func (f terminalFunc) SetStderr(io.Writer) {}
func tuiCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "tui",
		Short: "Open the interactive sandbox control panel",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTUI(cmd.Context())
		},
	}
}

func runTUI(ctx context.Context) error {
	_ = ctx
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return fmt.Errorf("mob tui requires an interactive terminal")
	}

	reader := bufio.NewReader(os.Stdin)
	cfg, err := loadCfg()
	if err != nil {
		ui.Warn("mob is not configured yet")
		ok, promptErr := confirmPrompt(reader, "Run mob init now?", true)
		if promptErr != nil {
			return promptErr
		}
		if !ok {
			ui.Info("Run 'mob init' before opening the TUI")
			return nil
		}
		if err := runInitInteractive(reader); err != nil {
			return err
		}
		cfg, err = loadCfg()
		if err != nil {
			return err
		}
	}

	if cfg.SSHPort == 0 {
		cfg.SSHPort = 2222
	}

	model := newTUIModel(cfg)
	finalModel, err := tea.NewProgram(model, tea.WithAltScreen()).Run()
	if final, ok := finalModel.(tuiModel); ok {
		final.stopAllForwards()
	}
	return err
}

func newTUIModel(cfg *config.ClientConfig) tuiModel {
	items := []list.Item{}
	delegate := list.NewDefaultDelegate()
	delegate.Styles.SelectedTitle = delegate.Styles.SelectedTitle.Foreground(lipgloss.Color("42")).BorderForeground(lipgloss.Color("42"))
	delegate.Styles.SelectedDesc = delegate.Styles.SelectedDesc.Foreground(lipgloss.Color("245")).BorderForeground(lipgloss.Color("42"))

	l := list.New(items, delegate, 80, 18)
	l.Title = "Sandboxes"
	l.SetShowHelp(false)
	l.SetFilteringEnabled(false)
	l.SetShowStatusBar(false)
	l.SetStatusBarItemName("sandbox", "sandboxes")

	in := textinput.New()
	in.Prompt = "> "
	in.CharLimit = 120

	spin := spinner.New(
		spinner.WithSpinner(spinner.MiniDot),
		spinner.WithStyle(lipgloss.NewStyle().Foreground(lipgloss.Color("42"))),
	)

	return tuiModel{
		cfg:        cfg,
		client:     newClient(cfg),
		list:       l,
		spinner:    spin,
		input:      in,
		mode:       modeBusy,
		busyLabel:  "Loading sandboxes",
		nextTunnel: 1,
		status:     "Loading sandboxes...",
		statusKind: "info",
	}
}

func (m tuiModel) Init() tea.Cmd {
	return tea.Batch(m.spinner.Tick, loadSandboxesCmd(m.client))
}

func (m tuiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.resize()
	case sandboxesLoadedMsg:
		m.mode = modeDashboard
		if msg.err != nil {
			m.setStatus("error", fmt.Sprintf("Refresh failed: %v", msg.err))
			return m, nil
		}
		cmd := m.setSandboxes(msg.sandboxes)
		m.setStatus("ok", fmt.Sprintf("Loaded %d sandboxes", len(msg.sandboxes)))
		return m, cmd
	case sandboxCreatedMsg:
		m.mode = modeDashboard
		if msg.err != nil {
			m.setStatus("error", fmt.Sprintf("Create failed: %v", msg.err))
			return m, nil
		}
		m.setStatus("ok", fmt.Sprintf("%s ready (%s)", msg.id, msg.duration.Round(time.Second)))
		return m, loadSandboxesCmd(m.client)
	case sshPreparedMsg:
		m.mode = modeDashboard
		if msg.err != nil {
			m.setStatus("error", fmt.Sprintf("SSH failed: %v", msg.err))
			return m, nil
		}
		label := "SSH"
		run := func() error {
			return remote.ConnectSandbox(m.cfg.SSHHost, m.cfg.SSHPort, msg.token)
		}
		if msg.claude {
			label = "Claude Code"
			run = func() error {
				return remote.RunSandboxCommand(m.cfg.SSHHost, m.cfg.SSHPort, msg.token, "cd ~ && exec /usr/local/bin/claude")
			}
		}
		m.setStatus("info", fmt.Sprintf("Opening %s in %s", label, shortID(msg.id)))
		return m, tea.Exec(terminalFunc{run: run}, func(err error) tea.Msg {
			return terminalFinishedMsg{label: label, err: err}
		})
	case terminalFinishedMsg:
		if msg.err != nil {
			m.setStatus("error", fmt.Sprintf("%s exited with error: %v", msg.label, msg.err))
		} else {
			m.setStatus("ok", fmt.Sprintf("%s session closed", msg.label))
		}
		return m, loadSandboxesCmd(m.client)
	case forwardStartedMsg:
		m.mode = modeDashboard
		if msg.err != nil {
			m.setStatus("error", fmt.Sprintf("Forward failed: %v", msg.err))
			return m, nil
		}
		m.forwards = append(m.forwards, msg.forward)
		m.nextTunnel++
		m.setStatus("ok", fmt.Sprintf("http://localhost:%d -> %s:%d", msg.forward.LocalPort, shortID(msg.forward.SandboxID), msg.forward.RemotePort))
	case exposeCreatedMsg:
		m.mode = modeDashboard
		if msg.err != nil {
			m.setStatus("error", fmt.Sprintf("Expose failed: %v", msg.err))
			return m, nil
		}
		m.setStatus("ok", strings.TrimSpace(msg.response))
	case sandboxDeletedMsg:
		m.mode = modeDashboard
		if msg.err != nil {
			m.setStatus("error", fmt.Sprintf("Delete failed: %v", msg.err))
			return m, nil
		}
		m.setStatus("ok", fmt.Sprintf("Deleted %s", msg.id))
		return m, loadSandboxesCmd(m.client)
	case powerDoneMsg:
		m.mode = modeDashboard
		if msg.err != nil {
			m.setStatus("error", fmt.Sprintf("Power %s failed: %v", msg.action, msg.err))
			return m, nil
		}
		m.setStatus("ok", strings.TrimSpace(msg.output))
	case browserOpenedMsg:
		m.mode = modeDashboard
		if msg.err != nil {
			m.setStatus("error", fmt.Sprintf("OpenHands failed: %v", msg.err))
		} else {
			m.setStatus("ok", "OpenHands opened")
		}
	}

	if m.mode == modeBusy {
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		if cmd != nil {
			cmds = append(cmds, cmd)
		}
		if key, ok := msg.(tea.KeyMsg); ok {
			switch key.String() {
			case "ctrl+c", "q":
				m.stopAllForwards()
				return m, tea.Quit
			}
		}
		return m, tea.Batch(cmds...)
	}

	if m.mode == modePrompt {
		next, cmd := m.updatePrompt(msg)
		return next, cmd
	}

	if key, ok := msg.(tea.KeyMsg); ok {
		switch key.String() {
		case "ctrl+c", "q":
			m.stopAllForwards()
			return m, tea.Quit
		case "?":
			m.showHelp = !m.showHelp
			return m, nil
		case "r":
			return m.withBusy("Refreshing sandboxes", loadSandboxesCmd(m.client))
		case "c", "n":
			return m.withBusy("Creating sandbox", createSandboxCmd(m.cfg, m.client))
		case "enter", "s":
			return m.prepareSSH(false)
		case "a":
			return m.prepareSSH(true)
		case "f":
			return m.askPort(promptForwardPort, "Forward selected sandbox", "Keep a localhost tunnel open while staying in this TUI")
		case "u":
			return m.askPort(promptURLPort, "Preview URL", "Generate a one-hour Daytona preview URL")
		case "e":
			return m.askPort(promptExposePort, "Expose route", "Create a permanent subdomain route")
		case "d", "backspace":
			return m.askDelete()
		case "x":
			return m.askStopTunnel()
		case "o":
			return m.openOpenHands()
		case "p":
			return m.askPower()
		}
	}

	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	return m, cmd
}

func (m tuiModel) View() string {
	header := titleStyle.Render("mob") + " " + mutedStyle.Render(fmt.Sprintf("%s · %s mode", m.cfg.Server, defaultString(m.cfg.Mode, "ip")))

	body := m.list.View()
	side := m.sideView()
	if m.width >= 110 {
		body = lipgloss.JoinHorizontal(lipgloss.Top, body, "  ", side)
	} else {
		body = lipgloss.JoinVertical(lipgloss.Left, body, side)
	}

	if m.mode == modeBusy {
		body = lipgloss.JoinVertical(lipgloss.Left, body, "", fmt.Sprintf("%s %s", m.spinner.View(), m.busyLabel))
	}
	if m.mode == modePrompt {
		body = lipgloss.JoinVertical(lipgloss.Left, body, "", m.promptView())
	}

	return appStyle.Width(maxInt(1, m.width)).Render(lipgloss.JoinVertical(lipgloss.Left, header, "", body, "", m.footerView()))
}

func (m tuiModel) sideView() string {
	var b strings.Builder
	b.WriteString(sectionStyle.Render("Actions"))
	b.WriteString("\n")
	actions := []string{
		"enter/s  SSH",
		"a        Claude Code",
		"c/n      create",
		"f        forward",
		"u        preview URL",
		"e        expose",
		"d        delete",
		"o        OpenHands",
		"p        power",
		"r        refresh",
		"x        stop tunnel",
		"?        help",
		"q        quit",
	}
	if !m.showHelp {
		actions = actions[:8]
		actions = append(actions, "?        more")
	}
	for _, line := range actions {
		b.WriteString("  ")
		b.WriteString(line)
		b.WriteString("\n")
	}

	b.WriteString("\n")
	b.WriteString(sectionStyle.Render("Tunnels"))
	b.WriteString("\n")
	if len(m.forwards) == 0 {
		b.WriteString("  none\n")
	} else {
		for _, f := range m.forwards {
			b.WriteString(fmt.Sprintf("  #%d %s:%d -> localhost:%d\n", f.ID, shortID(f.SandboxID), f.RemotePort, f.LocalPort))
		}
	}

	return panelStyle.Width(sidebarWidth(m.width)).Render(strings.TrimRight(b.String(), "\n"))
}

func (m tuiModel) promptView() string {
	help := m.prompt.help
	if help != "" {
		help = "\n" + mutedStyle.Render(help)
	}
	return promptStyle.Render(fmt.Sprintf("%s%s\n\n%s\n\n%s", sectionStyle.Render(m.prompt.title), help, m.input.View(), mutedStyle.Render("enter submit · esc cancel")))
}

func (m tuiModel) footerView() string {
	status := m.status
	if status == "" {
		status = "Ready"
	}
	style := infoStyle
	switch m.statusKind {
	case "ok":
		style = okStyle
	case "error":
		style = errorStyle
	}
	return style.Render(status)
}

func (m *tuiModel) resize() {
	sidebar := sidebarWidth(m.width)
	listWidth := m.width - sidebar - 8
	if m.width < 110 {
		listWidth = m.width - 4
	}
	if listWidth < 40 {
		listWidth = 40
	}
	listHeight := m.height - 10
	if listHeight < 8 {
		listHeight = 8
	}
	m.list.SetSize(listWidth, listHeight)
}

func (m tuiModel) updatePrompt(msg tea.Msg) (tea.Model, tea.Cmd) {
	if key, ok := msg.(tea.KeyMsg); ok {
		switch key.String() {
		case "esc", "ctrl+c":
			m.mode = modeDashboard
			m.input.Blur()
			m.setStatus("info", "Cancelled")
			return m, nil
		case "enter":
			return m.submitPrompt()
		}
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m tuiModel) submitPrompt() (tea.Model, tea.Cmd) {
	value := strings.TrimSpace(m.input.Value())
	prompt := m.prompt
	m.input.Blur()
	m.mode = modeDashboard

	switch prompt.kind {
	case promptForwardPort:
		port, err := parsePort(value)
		if err != nil {
			m.setStatus("error", err.Error())
			return m, nil
		}
		return m.withBusy("Opening tunnel", startForwardCmd(m.cfg, m.client, prompt.sandboxID, port, m.nextTunnel))
	case promptURLPort:
		port, err := parsePort(value)
		if err != nil {
			m.setStatus("error", err.Error())
			return m, nil
		}
		url := m.client.BuildPreviewURL(prompt.sandboxID, port, previewDomain(m.cfg))
		m.setStatus("ok", url+"  (auth via cookie, 1h)")
	case promptExposePort:
		port, err := parsePort(value)
		if err != nil {
			m.setStatus("error", err.Error())
			return m, nil
		}
		return m.ask(promptState{
			kind:      promptExposeName,
			title:     "Expose route name",
			help:      "Route names become permanent subdomains.",
			sandboxID: prompt.sandboxID,
			port:      port,
		}, defaultExposeName(prompt.sandboxID))
	case promptExposeName:
		if value == "" {
			m.setStatus("error", "route name is required")
			return m, nil
		}
		return m.withBusy("Creating route", createExposeCmd(m.cfg, prompt.sandboxID, prompt.port, value))
	case promptDelete:
		if strings.ToLower(value) != "delete" {
			m.setStatus("info", "Delete cancelled")
			return m, nil
		}
		return m.withBusy("Deleting sandbox", deleteSandboxCmd(m.client, prompt.sandboxID))
	case promptStopTunnel:
		id, err := strconv.Atoi(value)
		if err != nil {
			m.setStatus("error", "invalid tunnel number")
			return m, nil
		}
		return m.stopForward(id), nil
	case promptPowerAction:
		if value == "" {
			value = "status"
		}
		action := strings.ToLower(value)
		switch action {
		case "status", "start":
			return m.withBusy("Calling power worker", powerTeaCmd(action, m.cfg))
		case "stop", "reboot":
			return m.ask(promptState{
				kind:   promptPowerConfirm,
				title:  fmt.Sprintf("Confirm power %s", action),
				help:   fmt.Sprintf("Type %s to continue.", action),
				action: action,
			}, "")
		default:
			m.setStatus("error", "power action must be status, start, stop, or reboot")
		}
	case promptPowerConfirm:
		if strings.ToLower(value) != prompt.action {
			m.setStatus("info", "Power action cancelled")
			return m, nil
		}
		return m.withBusy("Calling power worker", powerTeaCmd(prompt.action, m.cfg))
	}

	return m, nil
}

func (m tuiModel) prepareSSH(claude bool) (tea.Model, tea.Cmd) {
	id := m.selectedSandboxID()
	if id == "" {
		m.setStatus("error", "No sandbox selected. Press c to create one.")
		return m, nil
	}
	label := "Preparing SSH"
	if claude {
		label = "Preparing Claude Code"
	}
	return m.withBusy(label, prepareSSHCmd(m.client, id, claude))
}

func (m tuiModel) askPort(kind promptKind, title, help string) (tea.Model, tea.Cmd) {
	if (kind == promptURLPort || kind == promptExposePort) && m.cfg.Mode == "ip" {
		m.setStatus("error", "Domain mode is required. Use forward in IP mode.")
		return m, nil
	}
	id := m.selectedSandboxID()
	if id == "" {
		m.setStatus("error", "No sandbox selected. Press c to create one.")
		return m, nil
	}
	return m.ask(promptState{
		kind:      kind,
		title:     title,
		help:      help,
		sandboxID: id,
	}, "3000")
}

func (m tuiModel) askDelete() (tea.Model, tea.Cmd) {
	id := m.selectedSandboxID()
	if id == "" {
		m.setStatus("error", "No sandbox selected")
		return m, nil
	}
	return m.ask(promptState{
		kind:      promptDelete,
		title:     fmt.Sprintf("Delete %s", shortID(id)),
		help:      "Type delete to confirm.",
		sandboxID: id,
	}, "")
}

func (m tuiModel) askStopTunnel() (tea.Model, tea.Cmd) {
	if len(m.forwards) == 0 {
		m.setStatus("info", "No active tunnels")
		return m, nil
	}
	if len(m.forwards) == 1 {
		return m.stopForward(m.forwards[0].ID), nil
	}
	return m.ask(promptState{
		kind:  promptStopTunnel,
		title: "Stop tunnel",
		help:  "Enter the tunnel number shown in the Tunnels panel.",
	}, strconv.Itoa(m.forwards[0].ID))
}

func (m tuiModel) askPower() (tea.Model, tea.Cmd) {
	return m.ask(promptState{
		kind:  promptPowerAction,
		title: "Power control",
		help:  "Actions: status, start, stop, reboot.",
	}, "status")
}

func (m tuiModel) ask(prompt promptState, value string) (tea.Model, tea.Cmd) {
	m.mode = modePrompt
	m.prompt = prompt
	m.input.SetValue(value)
	m.input.Placeholder = value
	m.input.CursorEnd()
	return m, m.input.Focus()
}

func (m tuiModel) openOpenHands() (tea.Model, tea.Cmd) {
	if m.cfg.OpenHands == "" {
		m.setStatus("error", "OpenHands URL is not configured")
		return m, nil
	}
	return m.withBusy("Opening OpenHands", func() tea.Msg {
		return browserOpenedMsg{err: openBrowser(m.cfg.OpenHands)}
	})
}

func (m tuiModel) stopForward(id int) tuiModel {
	for i, f := range m.forwards {
		if f.ID == id {
			f.cancel()
			m.forwards = append(m.forwards[:i], m.forwards[i+1:]...)
			m.setStatus("ok", fmt.Sprintf("Stopped tunnel %d", id))
			return m
		}
	}
	m.setStatus("error", fmt.Sprintf("Tunnel %d not found", id))
	return m
}

func (m *tuiModel) setStatus(kind, text string) {
	m.statusKind = kind
	m.status = text
}

func (m tuiModel) withBusy(label string, cmd tea.Cmd) (tea.Model, tea.Cmd) {
	m.mode = modeBusy
	m.busyLabel = label
	m.setStatus("info", label+"...")
	return m, tea.Batch(m.spinner.Tick, cmd)
}

func (m *tuiModel) setSandboxes(sandboxes []daytona.Sandbox) tea.Cmd {
	selected := m.selectedSandboxID()
	items := make([]list.Item, 0, len(sandboxes))
	for _, sb := range sandboxes {
		items = append(items, sandboxItem{id: sb.ID, state: sb.State})
	}
	cmd := m.list.SetItems(items)
	if len(items) == 0 {
		return cmd
	}
	if selected == "" {
		m.list.Select(0)
		return cmd
	}
	for i, item := range items {
		if item.(sandboxItem).id == selected {
			m.list.Select(i)
			return cmd
		}
	}
	m.list.Select(0)
	return cmd
}

func (m tuiModel) selectedSandboxID() string {
	item := m.list.SelectedItem()
	if item == nil {
		return ""
	}
	sb, ok := item.(sandboxItem)
	if !ok {
		return ""
	}
	return sb.id
}

func (m *tuiModel) stopAllForwards() {
	for _, f := range m.forwards {
		f.cancel()
	}
	m.forwards = nil
}

func loadSandboxesCmd(client *daytona.Client) tea.Cmd {
	return func() tea.Msg {
		sandboxes, err := client.ListSandboxes()
		if err == nil {
			sort.Slice(sandboxes, func(i, j int) bool {
				if sandboxes[i].State == sandboxes[j].State {
					return sandboxes[i].ID < sandboxes[j].ID
				}
				return sandboxes[i].State < sandboxes[j].State
			})
		}
		return sandboxesLoadedMsg{sandboxes: sandboxes, err: err}
	}
}

func createSandboxCmd(cfg *config.ClientConfig, client *daytona.Client) tea.Cmd {
	return func() tea.Msg {
		start := time.Now()
		sb, err := client.CreateSandboxWithEnv("mob-sandbox:1.0", sandboxEnv(cfg))
		if err != nil {
			return sandboxCreatedMsg{err: err}
		}
		if err := waitSandboxReady(client, sb.ID); err != nil {
			return sandboxCreatedMsg{err: err}
		}
		return sandboxCreatedMsg{id: sb.ID, duration: time.Since(start)}
	}
}

func prepareSSHCmd(client *daytona.Client, sandboxID string, claude bool) tea.Cmd {
	return func() tea.Msg {
		if err := ensureSandboxReadySilent(client, sandboxID); err != nil {
			return sshPreparedMsg{err: err}
		}
		access, err := client.GetSSHAccess(sandboxID)
		if err != nil {
			return sshPreparedMsg{err: fmt.Errorf("ssh access: %w", err)}
		}
		return sshPreparedMsg{id: sandboxID, token: access.Token, claude: claude}
	}
}

func startForwardCmd(cfg *config.ClientConfig, client *daytona.Client, sandboxID string, remotePort, tunnelID int) tea.Cmd {
	return func() tea.Msg {
		if err := ensureSandboxReadySilent(client, sandboxID); err != nil {
			return forwardStartedMsg{err: err}
		}
		access, err := client.GetSSHAccess(sandboxID)
		if err != nil {
			return forwardStartedMsg{err: err}
		}
		ctx, cancel := context.WithCancel(context.Background())
		localPort, err := remote.PortForward(ctx, cfg.SSHHost, cfg.SSHPort, access.Token, remotePort)
		if err != nil {
			cancel()
			return forwardStartedMsg{err: err}
		}
		return forwardStartedMsg{forward: activeForward{
			ID:         tunnelID,
			SandboxID:  sandboxID,
			RemotePort: remotePort,
			LocalPort:  localPort,
			cancel:     cancel,
		}}
	}
}

func createExposeCmd(cfg *config.ClientConfig, sandboxID string, port int, name string) tea.Cmd {
	return func() tea.Msg {
		body, err := json.Marshal(map[string]any{
			"sandbox_id": sandboxID,
			"port":       port,
			"name":       name,
		})
		if err != nil {
			return exposeCreatedMsg{err: err}
		}
		resp, err := makeControlRequest("POST", cfg.Control+"/control/v1/expose", string(body), cfg.APIKey)
		return exposeCreatedMsg{response: resp, err: err}
	}
}

func deleteSandboxCmd(client *daytona.Client, sandboxID string) tea.Cmd {
	return func() tea.Msg {
		err := client.DeleteSandbox(sandboxID)
		return sandboxDeletedMsg{id: sandboxID, err: err}
	}
}

func powerTeaCmd(action string, cfg *config.ClientConfig) tea.Cmd {
	return func() tea.Msg {
		out, err := power.Call(cfg.PowerWorkerURL, cfg.OperatorName, cfg.SSHKeyPath, action)
		return powerDoneMsg{action: action, output: out, err: err}
	}
}

func ensureSandboxReadySilent(client *daytona.Client, sandboxID string) error {
	sb, err := getSandboxWithRetry(client, sandboxID, 3, 2*time.Second)
	if err != nil {
		return err
	}
	if isSandboxReady(sb.State) {
		return nil
	}
	if err := client.StartSandbox(sandboxID); err != nil {
		return fmt.Errorf("start sandbox: %w", err)
	}
	return waitSandboxReady(client, sandboxID)
}

func parsePort(value string) (int, error) {
	port, err := strconv.Atoi(value)
	if err != nil || port <= 0 || port > 65535 {
		return 0, fmt.Errorf("enter a port between 1 and 65535")
	}
	return port, nil
}

func shortID(id string) string {
	if len(id) <= 12 {
		return id
	}
	return id[:12]
}

func defaultString(value, def string) string {
	if value == "" {
		return def
	}
	return value
}

func sidebarWidth(width int) int {
	if width < 110 {
		return maxInt(40, width-4)
	}
	return 34
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

var (
	appStyle     = lipgloss.NewStyle().Padding(1, 2)
	titleStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("42"))
	mutedStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	sectionStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("81"))
	panelStyle   = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("238")).Padding(1, 2)
	promptStyle  = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("42")).Padding(1, 2)
	infoStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("81"))
	okStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	errorStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
)
