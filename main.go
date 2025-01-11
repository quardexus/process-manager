package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// -----------------------------------------------------------------------------
// Global variable to access the model
// -----------------------------------------------------------------------------
var globalModel *model

// -----------------------------------------------------------------------------
// Configuration
// -----------------------------------------------------------------------------

type ProcessConfig struct {
	CmdLine     string `json:"cmd_line"`
	AutoRestart bool   `json:"auto_restart"`
}

type Config struct {
	Processes []ProcessConfig `json:"processes"`
}

// -----------------------------------------------------------------------------
// Process data
// -----------------------------------------------------------------------------

type ProcessItem struct {
	CmdLine     string
	AutoRestart bool
	Logs        []string
	Cmd         *exec.Cmd
	Finished    bool

	stdout io.ReadCloser
	stderr io.ReadCloser
	mu     sync.Mutex
}

type processListItem struct {
	p *ProcessItem
}

func (it processListItem) Title() string       { return it.p.CmdLine }
func (it processListItem) Description() string { return "" }
func (it processListItem) FilterValue() string { return it.p.CmdLine }

type newProcessItem struct{}

func (ni newProcessItem) Title() string       { return "[Create a new process]" }
func (ni newProcessItem) Description() string { return "" }
func (ni newProcessItem) FilterValue() string { return "" }

// -----------------------------------------------------------------------------
// Custom delegate for the list with a second column
// -----------------------------------------------------------------------------

type customDelegate struct {
	list.DefaultDelegate
	selectionColumn int
}

func (d customDelegate) Render(w io.Writer, m list.Model, index int, listItem list.Item) {
	item, ok := listItem.(processListItem)
	if !ok {
		d.DefaultDelegate.Render(w, m, index, listItem)
		return
	}

	str := item.Title()
	if d.selectionColumn == 1 {
		str += "   [x]"
	}

	var out string
	if index == m.Index() {
		out = lipgloss.NewStyle().Foreground(lipgloss.Color("205")).Render(str)
	} else {
		out = str
	}
	fmt.Fprintln(w, out)
}

// -----------------------------------------------------------------------------
// States
// -----------------------------------------------------------------------------

type state int

const (
	stateList state = iota
	stateAddProcess
	stateViewLogs
)

// -----------------------------------------------------------------------------
// Log update message
// -----------------------------------------------------------------------------

type logUpdateMsg struct {
	processIndex int
}

// -----------------------------------------------------------------------------
// Main Bubble Tea model
// -----------------------------------------------------------------------------

type model struct {
	state               state
	list                list.Model
	textInput           textinput.Model
	viewport            viewport.Model
	processes           []*ProcessItem
	currentProcessIndex int
	configPath          string

	justEnteredLogs bool
	program         *tea.Program

	delegate        customDelegate
	selectionColumn int

	newProcAutoRestart bool

	windowWidth  int
	windowHeight int
}

// -----------------------------------------------------------------------------
// Constants and functions
// -----------------------------------------------------------------------------

const maxLogLines = 1000

type tickMsg time.Time

func tickCmd() tea.Cmd {
	return tea.Tick(time.Millisecond*300, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

func loadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func saveConfig(path string, cfg *Config) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

func createDefaultConfig(path string) (*Config, error) {
	cfg := &Config{Processes: []ProcessConfig{}}
	if err := saveConfig(path, cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

func appendLog(p *ProcessItem, line string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.Logs = append(p.Logs, line)
	if len(p.Logs) > maxLogLines {
		p.Logs = p.Logs[len(p.Logs)-maxLogLines:]
	}
}

func startProcess(program *tea.Program, processIndex int, cmdLine string, autoRestart bool) (*ProcessItem, error) {
	parts := strings.Fields(cmdLine)
	if len(parts) == 0 {
		return nil, errors.New("empty command line")
	}

	var startSingleProcess func() error
	pItem := &ProcessItem{
		CmdLine:     cmdLine,
		AutoRestart: autoRestart,
		Logs:        []string{},
	}

	startSingleProcess = func() error {
		cmd := exec.Command(parts[0], parts[1:]...)

		stdout, err := cmd.StdoutPipe()
		if err != nil {
			return fmt.Errorf("failed to create stdout pipe: %v", err)
		}
		stderr, err := cmd.StderrPipe()
		if err != nil {
			return fmt.Errorf("failed to create stderr pipe: %v", err)
		}

		pItem.mu.Lock()
		pItem.Cmd = cmd
		pItem.stdout = stdout
		pItem.stderr = stderr
		pItem.Finished = false
		pItem.mu.Unlock()

		if err := cmd.Start(); err != nil {
			return fmt.Errorf("failed to start process: %v", err)
		}

		outputDone := &sync.WaitGroup{}
		outputDone.Add(2)

		go func() {
			defer outputDone.Done()
			if err := pItem.readOutput(program, processIndex, stdout); err != nil {
				appendLog(pItem, fmt.Sprintf("[Error reading stdout: %v]", err))
			}
		}()

		go func() {
			defer outputDone.Done()
			if err := pItem.readOutput(program, processIndex, stderr); err != nil {
				appendLog(pItem, fmt.Sprintf("[Error reading stderr: %v]", err))
			}
		}()

		go func() {
			_ = cmd.Wait()
			outputDone.Wait()

			pItem.mu.Lock()
			pItem.Finished = true
			pItem.mu.Unlock()

			appendLog(pItem, "[Process finished]")

			if pItem.AutoRestart {
				appendLog(pItem, "[Attempting to restart process...]")
				time.Sleep(time.Second)

				if err := startSingleProcess(); err != nil {
					appendLog(pItem, fmt.Sprintf("[Failed to restart process: %v]", err))
				} else {
					appendLog(pItem, "[Process restarted successfully]")
				}
			}
		}()

		return nil
	}

	if err := startSingleProcess(); err != nil {
		return nil, err
	}

	return pItem, nil
}

func (p *ProcessItem) readOutput(program *tea.Program, processIndex int, r io.Reader) error {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := scanner.Text()

		utf8Line, err := cp866ToUTF8(line)
		if err != nil {
			utf8Line = line
		}

		appendLog(p, utf8Line)

		if program != nil {
			program.Send(logUpdateMsg{processIndex: processIndex})
		}
	}

	return scanner.Err()
}

func initialModel(program *tea.Program) model {
	exePath, err := os.Executable()
	if err != nil {
		log.Fatal(err)
	}
	dir := filepath.Dir(exePath)
	configPath := filepath.Join(dir, "process_manager_config.json")

	cfg, err := loadConfig(configPath)
	if os.IsNotExist(err) {
		cfg, err = createDefaultConfig(configPath)
		if err != nil {
			log.Fatal("Failed to create config:", err)
		}
	} else if err != nil {
		// If failed to read the existing config, rename it
		backupPath := configPath + ".old"
		renameErr := os.Rename(configPath, backupPath)
		if renameErr != nil {
			log.Fatalf("Failed to rename invalid config: %v", renameErr)
		}
		log.Printf("Invalid config renamed to %s", backupPath)
		cfg, err = createDefaultConfig(configPath)
		if err != nil {
			log.Fatal("Failed to create new config:", err)
		}
	}

	var processes []*ProcessItem
	for i, procCfg := range cfg.Processes {
		pItem, err := startProcess(nil, i, procCfg.CmdLine, procCfg.AutoRestart)
		if err != nil {
			log.Printf("Error starting '%s': %v\n", procCfg.CmdLine, err)
			continue
		}
		processes = append(processes, pItem)
	}

	delegate := customDelegate{
		DefaultDelegate: list.NewDefaultDelegate(),
		selectionColumn: 0,
	}

	var items []list.Item
	for _, pr := range processes {
		items = append(items, processListItem{p: pr})
	}
	items = append(items, newProcessItem{})

	l := list.New(items, delegate, 50, 12)
	l.Title = "Quardexus Process Manager v1.0 [beta2]"
	l.SetShowHelp(true)
	l.SetShowStatusBar(false)
	l.SetShowFilter(true)
	l.SetShowPagination(true)

	ti := textinput.New()
	ti.Placeholder = "Enter a command..."
	ti.Focus()

	vp := viewport.New(0, 0)
	vp.Style = lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("240"))
	vp.MouseWheelEnabled = true

	return model{
		state:               stateList,
		list:                l,
		textInput:           ti,
		viewport:            vp,
		processes:           processes,
		currentProcessIndex: -1,
		configPath:          configPath,
		justEnteredLogs:     false,
		program:             program,
		delegate:            delegate,
		selectionColumn:     0,
		newProcAutoRestart:  true,
		windowWidth:         0,
		windowHeight:        0,
	}
}

// -----------------------------------------------------------------------------
// Update
// -----------------------------------------------------------------------------

func (m model) Init() tea.Cmd {
	return tea.Batch(
		tickCmd(),
	)
}

func (m model) killAllProcesses() {
	for _, pItem := range m.processes {
		if !pItem.Finished && pItem.Cmd != nil && pItem.Cmd.Process != nil {
			_ = pItem.Cmd.Process.Kill()
			appendLog(pItem, "[Process killed by exit]")
		}
	}
}

func (m *model) removeProcess(index int) {
	if index < 0 || index >= len(m.processes) {
		return
	}
	proc := m.processes[index]
	if !proc.Finished && proc.Cmd != nil && proc.Cmd.Process != nil {
		_ = proc.Cmd.Process.Kill()
		appendLog(proc, "[Process killed for removal]")
	}

	m.processes = append(m.processes[:index], m.processes[index+1:]...)

	var newItems []list.Item
	for _, pr := range m.processes {
		newItems = append(newItems, processListItem{p: pr})
	}
	newItems = append(newItems, newProcessItem{})
	m.list.SetItems(newItems)

	cfg, err := loadConfig(m.configPath)
	if err != nil {
		log.Println("Error loading config:", err)
		return
	}

	var procs []ProcessConfig
	for _, p := range m.processes {
		procs = append(procs, ProcessConfig{
			CmdLine:     p.CmdLine,
			AutoRestart: p.AutoRestart,
		})
	}
	cfg.Processes = procs
	if err := saveConfig(m.configPath, cfg); err != nil {
		log.Println("Error saving config:", err)
	} else {
		log.Println("Configuration saved successfully after removal.")
	}
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch m.state {
	case stateList:
		switch msg := msg.(type) {
		case tea.KeyMsg:
			switch msg.String() {
			case "ctrl+c", "q":
				m.killAllProcesses()
				return m, tea.Quit

			case "right":
				m.selectionColumn = 1
				m.delegate.selectionColumn = 1
				m.list.SetDelegate(m.delegate)
				return m, nil

			case "left":
				m.selectionColumn = 0
				m.delegate.selectionColumn = 0
				m.list.SetDelegate(m.delegate)
				return m, nil

			case "enter":
				if m.selectionColumn == 1 {
					idx := m.list.Index()
					if idx >= 0 && idx < len(m.list.Items()) {
						switch m.list.Items()[idx].(type) {
						case processListItem:
							m.removeProcess(idx)
							m.selectionColumn = 0
							m.delegate.selectionColumn = 0
							m.list.SetDelegate(m.delegate)
							return m, nil
						case newProcessItem:
							m.state = stateAddProcess
							m.textInput.SetValue("")
							m.newProcAutoRestart = true
							return m, nil
						}
					}
				} else {
					idx := m.list.Index()
					if idx < 0 || idx >= len(m.list.Items()) {
						return m, nil
					}
					itm := m.list.Items()[idx]

					switch itm.(type) {
					case newProcessItem:
						m.state = stateAddProcess
						m.textInput.SetValue("")
						m.newProcAutoRestart = true
						return m, nil

					case processListItem:
						m.currentProcessIndex = idx
						m.state = stateViewLogs
						m.justEnteredLogs = true

						if m.windowWidth > 0 && m.windowHeight > 0 {
							m.viewport.Width = m.windowWidth
							m.viewport.Height = m.windowHeight - 5
						}

						p := m.processes[m.currentProcessIndex]
						now := time.Now().Format("2006-01-02 15:04:05")
						appendLog(p, fmt.Sprintf("%s [Logs window opened: %s]", now, p.CmdLine))

						m = m.updateLogsViewport(true)
						return m, nil
					}
				}

			}

		case tickMsg:
			return m, tickCmd()

		case tea.WindowSizeMsg:
			m.windowWidth = msg.Width
			m.windowHeight = msg.Height
			newHeight := msg.Height - 5
			if newHeight < 3 {
				newHeight = 3
			}
			m.list.SetHeight(newHeight)
			return m, nil
		}

		m.list, _ = m.list.Update(msg)
		return m, nil

	case stateAddProcess:
		switch msg := msg.(type) {
		case tea.KeyMsg:
			switch msg.String() {
			case "esc":
				m.state = stateList
				return m, nil

			case "ctrl+c", "q":
				m.killAllProcesses()
				return m, tea.Quit

			case "enter":
				cmdLine := strings.TrimSpace(m.textInput.Value())
				if cmdLine != "" {
					pItem, err := startProcess(m.program, len(m.processes), cmdLine, m.newProcAutoRestart)
					if err == nil {
						m.processes = append(m.processes, pItem)

						cfg, err := loadConfig(m.configPath)
						if err != nil {
							log.Println("Error loading config:", err)
						} else {
							cfg.Processes = append(cfg.Processes, ProcessConfig{
								CmdLine:     cmdLine,
								AutoRestart: m.newProcAutoRestart,
							})
							if err := saveConfig(m.configPath, cfg); err != nil {
								log.Println("Error saving config:", err)
							} else {
								log.Println("Configuration saved successfully after adding process.")
							}
						}

						var newItems []list.Item
						for _, pr := range m.processes {
							newItems = append(newItems, processListItem{p: pr})
						}
						newItems = append(newItems, newProcessItem{})
						m.list.SetItems(newItems)

						m.currentProcessIndex = len(m.processes) - 1
						m.state = stateViewLogs
						m.justEnteredLogs = true

						if m.windowWidth > 0 && m.windowHeight > 0 {
							m.viewport.Width = m.windowWidth
							m.viewport.Height = m.windowHeight - 5
						}

						m = m.updateLogsViewport(true)
						return m, nil
					} else {
						log.Println("Error launching process:", err)
					}
				}
				m.state = stateList
				return m, nil

			case "tab":
				m.newProcAutoRestart = !m.newProcAutoRestart
				return m, nil
			}

		case tickMsg:
			return m, tickCmd()

		case tea.WindowSizeMsg:
			m.windowWidth = msg.Width
			m.windowHeight = msg.Height
			return m, nil
		}

		m.textInput, _ = m.textInput.Update(msg)
		return m, nil

	case stateViewLogs:
		switch msg := msg.(type) {
		case tea.KeyMsg:
			switch msg.String() {
			case "esc":
				m.state = stateList
				return m, nil

			case "ctrl+c", "q":
				m.killAllProcesses()
				return m, tea.Quit

			case "up", "k":
				m.viewport.LineUp(1)
			case "down", "j":
				m.viewport.LineDown(1)
			case "pgup":
				m.viewport.HalfViewUp()
			case "pgdown":
				m.viewport.HalfViewDown()
			case "home":
				m.viewport.GotoTop()
			case "end":
				m.viewport.GotoBottom()
			}

		case tea.MouseMsg:
			switch msg.Type {
			case tea.MouseWheelUp:
				m.viewport.LineUp(3)
			case tea.MouseWheelDown:
				m.viewport.LineDown(3)
			}

		case tickMsg:
			m = m.updateLogsViewport(false)
			return m, tickCmd()

		case logUpdateMsg:
			if m.currentProcessIndex == msg.processIndex {
				m = m.updateLogsViewport(m.viewport.AtBottom())
			}
			return m, nil

		case tea.WindowSizeMsg:
			m.windowWidth = msg.Width
			m.windowHeight = msg.Height

			m.viewport.Width = msg.Width
			m.viewport.Height = msg.Height - 5

			m = m.updateLogsViewport(false)
			return m, nil
		}

		m.viewport, _ = m.viewport.Update(msg)
		return m, nil
	}

	return m, nil
}

// -----------------------------------------------------------------------------
// View
// -----------------------------------------------------------------------------

func (m model) View() string {
	switch m.state {
	case stateList:
		helpText := "\n\nUse ←/→ to switch between modes. Right arrow: switch to process deletion mode."
		return m.list.View() + helpText

	case stateAddProcess:
		checkbox := "[ ]"
		if m.newProcAutoRestart {
			checkbox = "[x]"
		}
		return lipgloss.NewStyle().Padding(1, 2).Render(
			fmt.Sprintf(
				"Enter a command for a new process:\n\n%s\n\nAuto-restart: %s (Tab to toggle)\n\n(Enter – start, Esc – cancel, q/ctrl+c – quit)",
				m.textInput.View(),
				checkbox,
			),
		)

	case stateViewLogs:
		if m.currentProcessIndex < 0 || m.currentProcessIndex >= len(m.processes) {
			return "Error: invalid process index"
		}
		proc := m.processes[m.currentProcessIndex]

		proc.mu.Lock()
		linesCount := len(proc.Logs)
		proc.mu.Unlock()

		return lipgloss.JoinVertical(
			lipgloss.Left,
			fmt.Sprintf("Viewing logs for process '%s' (total lines: %d)", proc.CmdLine, linesCount),
			"Controls: Esc – back, ↑/↓ – scroll, Home/End – top/bottom, q/ctrl+c – quit",
			m.viewport.View(),
		)
	}

	return "Unknown state"
}

func (m model) updateLogsViewport(forceGotoBottom bool) model {
	if m.currentProcessIndex < 0 || m.currentProcessIndex >= len(m.processes) {
		return m
	}
	proc := m.processes[m.currentProcessIndex]

	proc.mu.Lock()
	logs := strings.Join(proc.Logs, "\n")
	proc.mu.Unlock()

	currentTime := time.Now().Format("2006-01-02 15:04:05")
	header := fmt.Sprintf("Current time: %s\n", currentTime)
	linesInfo := fmt.Sprintf("Number of lines: %d\n---\n", len(proc.Logs))
	content := header + linesInfo + logs

	oldYOffset := m.viewport.YOffset
	wasAtBottom := m.viewport.AtBottom()

	m.viewport.SetContent(content)

	if forceGotoBottom || m.justEnteredLogs {
		m.viewport.GotoBottom()
		m.justEnteredLogs = false
		return m
	}
	if wasAtBottom {
		m.viewport.GotoBottom()
	} else {
		m.viewport.YOffset = oldYOffset
	}
	return m
}

func cp866ToUTF8(s string) (string, error) {
	// Placeholder for code page conversion
	return s, nil
}

func (m *model) SetProgram(p *tea.Program) {
	m.program = p
}

func main() {
	if runtime.GOOS == "windows" {
		_ = exec.Command("cmd", "/c", "chcp", "65001").Run()
	}

	m := initialModel(nil)

	p := tea.NewProgram(
		m,
		tea.WithAltScreen(),
	)

	globalModel = &m

	m.SetProgram(p)

	if err := p.Start(); err != nil {
		fmt.Println("Error:", err)
		os.Exit(1)
	}
}
