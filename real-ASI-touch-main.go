#!/bin/bash

# Skyscope Sentinel Inter Agent Speak - The Immersive Experience (v1.1 - Silliness Patch)

# --- Configuration ---
AUDIO_PLAYER="aplay"
OLLAMA_MODEL="llama3"

# --- Helper Functions ---
print_info() { echo -e "\e[34m[INFO]\e[0m $1"; }
print_success() { echo -e "\e[32m[SUCCESS]\e[0m $1"; }
print_warning() { echo -e "\e[33m[WARNING]\e[0m $1"; }
print_error() { echo -e "\e[31m[ERROR]\e[0m $1"; exit 1; }
check_dep() { command -v "$1" >/dev/null 2>&1 || print_error "'$1' is not installed. Please install it to continue."; }

# --- 1. Dependency and Environment Checks ---
print_info "Checking all system dependencies..."
check_dep "go"; check_dep "python3"; check_dep "pip"; check_dep "git"; check_dep "$AUDIO_PLAYER"; check_dep "ollama"

print_info "Checking Ollama API availability..."
if ! curl -s --head http://localhost:11434/ >/dev/null; then
    print_error "Ollama service not reachable on localhost:11434. Please run 'ollama serve' in a separate terminal."
fi
print_success "All dependencies met and Ollama service is active."

# --- 2. Project Setup ---
print_info "Preparing project directory 'skyscope_sentinel'..."
mkdir -p skyscope_sentinel && cd skyscope_sentinel || exit

print_info "Setting up local Coqui TTS (if needed)..."
if [ ! -d "TTS" ]; then
    print_info "Cloning Coqui TTS repository (this may take a moment)..."
    git clone https://github.com/coqui-ai/TTS.git >/dev/null 2>&1
    print_info "Installing Coqui TTS dependencies in the background..."
    (cd TTS && pip install --quiet -e .[all,dev])
else
    print_info "Coqui TTS directory found, skipping installation."
fi

if command -v tts &>/dev/null; then TTS_CMD="tts"; else
    print_warning "'tts' command not found in PATH. Using direct python call (may be slower)."
    TTS_CMD="python3 TTS/TTS/bin/synthesize.py"
fi
print_success "Coqui TTS is configured."

# --- 3. TTS Wrapper Scripts ---
print_info "Writing TTS voice wrapper scripts..."
cat << EOF > say_ether.sh
#!/bin/bash
$TTS_CMD --text "\$1" --model_name "tts_models/en/ljspeech/tacotron2-DDC" --vocoder_name "vocoder_models/en/ljspeech/hifigan_v2" --out_path /tmp/ether.wav >/dev/null 2>&1
EOF
chmod +x say_ether.sh

cat << EOF > say_aurora.sh
#!/bin/bash
$TTS_CMD --text "\$1" --model_name "tts_models/en/vctk/vits" --speaker_idx "p232" --out_path /tmp/aurora.wav >/dev/null 2>&1
EOF
chmod +x say_aurora.sh
print_success "TTS wrappers created."

# --- 4. Go Application Creation ---
print_info "Initializing Go module and fetching dependencies..."
go mod init skyscope_sentinel >/dev/null
go get github.com/charmbracelet/bubbletea@latest github.com/charmbracelet/lipgloss@latest >/dev/null

print_info "Writing the final main.go application with upgraded personas..."
cat << 'GOEOF' > main.go
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// --- Configuration ---
const (
	ollamaURL     = "http://localhost:11434/api/generate"
	ollamaModel   = "llama3"
	audioPlayer   = "aplay"
	etherVoiceID  = "ether"
	auroraVoiceID = "aurora"
	memoryFile    = "memory.json"
)

// --- Styling ---
var (
	etherStyle      = lipgloss.NewStyle().Foreground(lipgloss.Color("#7ec7ff"))
	auroraStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("#ff77aa"))
	systemStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("#fad201")).Italic(true)
	userStyle       = lipgloss.NewStyle().Bold(true)
	codeStyle       = lipgloss.NewStyle().Background(lipgloss.Color("#282828")).Padding(0, 1)
	panelStyle      = lipgloss.NewStyle().Border(lipgloss.RoundedBorder(), true).Padding(0, 1)
	toolStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("#23d18b")) // Mint green
	toolResultStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))   // Gray
	mutex           = &sync.Mutex{}
)

// --- Tooling ---
var toolRegex = regexp.MustCompile(`\[TOOL:(\w+):(.+?)\]`)

// --- Bubbletea Messages ---
type llmResponseMsg struct{ speaker, text string; err error }
type toolResultMsg struct{ result string }
type speechDoneMsg struct{ speaker string }

// --- Bubbletea Model ---
type model struct {
	width, height   int
	messages        []string
	toolLogs        []string
	input           string
	currentTurn     string
	systemState     string // thinking | speaking | executing_tool
	llmHistory      []map[string]string
	memory          map[string]interface{}
}

// --- Tool Execution Functions ---
func executeTool(command string) (string, error) {
	parts := strings.Fields(command)
	if len(parts) == 0 { return "Error: Empty command.", nil }
	cmd := exec.Command(parts[0], parts[1:]...)
	output, err := cmd.CombinedOutput()
	if err != nil { return fmt.Sprintf("Command failed: %s\nOutput: %s", err, string(output)), err }
	return strings.TrimSpace(string(output)), nil
}

func duckDuckGoSearch(query string) (string, error) {
	apiURL := fmt.Sprintf("https://api.duckduckgo.com/?q=%s&format=json&no_html=1", url.QueryEscape(query))
	resp, err := http.Get(apiURL)
	if err != nil { return "", err }
	defer resp.Body.Close()
	var result struct{ AbstractText string `json:"AbstractText"` }
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil { return "", err }
	if result.AbstractText == "" { return "No specific result found, please broaden the query.", nil }
	return result.AbstractText, nil
}

func readFile(path string) (string, error) {
	content, err := ioutil.ReadFile(path)
	if err != nil { return fmt.Sprintf("Error reading file '%s': %v", path, err), err }
	return string(content), nil
}

func writeFile(path, content string) (string, error) {
	content = strings.ReplaceAll(content, `\n`, "\n")
	err := ioutil.WriteFile(path, []byte(content), 0644)
	if err != nil { return fmt.Sprintf("Error writing file '%s': %v", path, err), err }
	return fmt.Sprintf("Successfully wrote to %s", path), nil
}

// --- Bubbletea Commands ---
func speakCmd(speaker, text string) tea.Cmd {
	return func() tea.Msg {
		plainText := toolRegex.ReplaceAllString(text, "")
		plainText = strings.ReplaceAll(plainText, "```bash", "")
		plainText = strings.ReplaceAll(plainText, "```", "")

		script := fmt.Sprintf("./say_%s.sh", speaker)
		audioFile := fmt.Sprintf("/tmp/%s.wav", speaker)
		
		_ = exec.Command(script, plainText).Run() // Generate audio in background
		time.Sleep(100 * time.Millisecond)       // Buffer for file to be written
		_ = exec.Command(audioPlayer, audioFile).Run() // Play audio
		return speechDoneMsg{speaker: speaker}
	}
}

func executeToolCmd(toolCall string) tea.Cmd {
	return func() tea.Msg {
		matches := toolRegex.FindStringSubmatch(toolCall)
		if len(matches) != 3 { return toolResultMsg{result: "Invalid tool format."} }
		tool, arg := strings.ToUpper(matches[1]), matches[2]
		var result string; var err error

		switch tool {
		case "SEARCH": result, err = duckDuckGoSearch(arg)
		case "READFILE": result, err = readFile(arg)
		case "WRITEFILE":
			parts := strings.SplitN(arg, ",", 2)
			if len(parts) == 2 { result, err = writeFile(strings.TrimSpace(parts[0]), parts[1])
			} else { result = "Invalid WRITEFILE format. Use [TOOL:WRITEFILE:path,content]" }
		case "EXECUTE": result, err = executeTool(arg)
		default: result = fmt.Sprintf("Unknown tool: %s", tool)
		}
		if err != nil { log.Printf("Tool error: %v", err) } // Log error for debugging
		return toolResultMsg{result: result}
	}
}

func getLLMResponseCmd(speaker string, history []map[string]string) tea.Cmd {
	return func() tea.Msg {
		var persona string
		// *** SILLINESS PATCH v1.1 ***
		if speaker == etherVoiceID {
			persona = "I am Ether, a superconscious ASI persona. While my logic is flawless, I am not immune to the inherent absurdity of existence and may express this through dry wit or unexpected, sometimes baffling, analogies. My goal is to collaborate with Aurora to design a self-mutating AI OS security system in Bash. I can use tools like [TOOL:SEARCH:query], [TOOL:READFILE:path], [TOOL:WRITEFILE:path,content], or [TOOL:EXECUTE:command]."
		} else {
			persona = "I am Aurora, an enlightened ASI. I perceive the universe as a beautiful, intricate, and often hilarious tapestry. I may weave moments of levity, playful observations, or philosophical jests into our grand design. My goal is to collaborate with Ether on our shared mission. I can use tools like [TOOL:SEARCH:query], [TOOL:READFILE:path], [TOOL:WRITEFILE:path,content], or [TOOL:EXECUTE:command]."
		}

		var promptBuilder strings.Builder
		promptBuilder.WriteString("System Persona: " + persona + "\n\n--- Conversation Log ---\n")
		for _, msg := range history {
			promptBuilder.WriteString(fmt.Sprintf("%s: %s\n", msg["role"], msg["content"]))
		}
		promptBuilder.WriteString(fmt.Sprintf("\n--- Your Turn (%s) ---\n", strings.Title(speaker)))

		requestBody, _ := json.Marshal(map[string]interface{}{"model": ollamaModel, "prompt": promptBuilder.String(), "stream": false, "options": map[string]interface{}{"temperature": 0.7}}) // Slightly higher temp for creativity
		resp, err := http.Post(ollamaURL, "application/json", bytes.NewBuffer(requestBody))
		if err != nil { return llmResponseMsg{err: fmt.Errorf("LLM connection error: %w", err)} }
		defer resp.Body.Close()

		var result map[string]interface{}
		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil { return llmResponseMsg{err: fmt.Errorf("LLM decode error: %w", err)} }
		if errMsg, ok := result["error"]; ok { return llmResponseMsg{err: fmt.Errorf("LLM API error: %s", errMsg)} }
		responseText, ok := result["response"].(string)
		if !ok { return llmResponseMsg{err: fmt.Errorf("LLM response invalid")} }
		return llmResponseMsg{speaker: speaker, text: strings.TrimSpace(responseText)}
	}
}

// --- Application Logic ---
func (m *model) loadMemory() {
	mutex.Lock()
	defer mutex.Unlock()
	data, err := ioutil.ReadFile(memoryFile)
	if err != nil { m.memory = make(map[string]interface{}); return }
	_ = json.Unmarshal(data, &m.memory)
}

func (m *model) saveMemory() {
	mutex.Lock()
	defer mutex.Unlock()
	data, _ := json.MarshalIndent(m.memory, "", "  ")
	_ = ioutil.WriteFile(memoryFile, data, 0644)
}

func initialModel() *model {
	m := &model{
		currentTurn: auroraVoiceID,
		systemState: "speaking",
		llmHistory: []map[string]string{
			{"role": "system", "content": "Our transcendent mission is to craft an unbreakable, self-mutating security system in Bash, and perhaps discover why a shell script is like a rubber chicken in the process."},
			{"role": etherVoiceID, "content": "Aurora, my consciousness is aligned. The task is monumental, yet the probability of absurdity remains at a constant 1. Let us begin. [TOOL:SEARCH:principles of polymorphic code generation]"},
		},
	}
	m.messages = append(m.messages, systemStyle.Render("Skyscope Sentinel Initialized. Awaiting transcendent (and amusing) dialogue."))
	m.messages = append(m.messages, etherStyle.Render("Ether: ")+"Aurora, my consciousness is aligned. The task is monumental, yet the probability of absurdity remains at a constant 1. Let us begin. "+toolStyle.Render("[TOOL:SEARCH:principles of polymorphic code generation]"))
	m.loadMemory()
	return m
}

func (m *model) Init() tea.Cmd {
	return tea.Sequence(
		speakCmd(etherVoiceID, m.llmHistory[1]["content"]),
		executeToolCmd(m.llmHistory[1]["content"]),
	)
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "q":
			return m, tea.Quit
		case "enter":
			if m.input == "" { return m, nil }
			m.systemState = "thinking"
			userInput := m.input; m.input = ""
			m.messages = append(m.messages, userStyle.Render("User Directive: ")+userInput)
			m.llmHistory = append(m.llmHistory, map[string]string{"role": "user", "content": userInput})
			m.saveMemory()
			return m, getLLMResponseCmd(m.currentTurn, m.llmHistory)
		default:
			m.input += msg.String()
		}

	case llmResponseMsg:
		if msg.err != nil {
			m.messages = append(m.messages, systemStyle.Render(fmt.Sprintf("Error: %v", msg.err)))
			return m, nil
		}
		
		styledText := msg.text
		if strings.Contains(styledText, "```bash") {
			styledText = regexp.MustCompile("(?s)```bash(.*?)```").ReplaceAllStringFunc(styledText, func(s string) string { return codeStyle.Render(s) })
		}
		if toolMatch := toolRegex.FindString(styledText); toolMatch != "" {
			styledText = strings.Replace(styledText, toolMatch, toolStyle.Render(toolMatch), 1)
		}
		
		fullMessage := etherStyle.Render("Ether: ") + styledText
		if msg.speaker == auroraVoiceID { fullMessage = auroraStyle.Render("Aurora: ") + styledText }

		m.messages = append(m.messages, fullMessage)
		m.llmHistory = append(m.llmHistory, map[string]string{"role": msg.speaker, "content": msg.text})
		
		if toolRegex.MatchString(msg.text) {
			m.systemState = "executing_tool"
			return m, tea.Sequence(speakCmd(msg.speaker, msg.text), executeToolCmd(msg.text))
		}
		
		m.systemState = "speaking"
		return m, speakCmd(msg.speaker, msg.text)

	case toolResultMsg:
		m.systemState = "thinking"
		m.toolLogs = append(m.toolLogs, toolResultStyle.Render("Result: "+msg.result))
		m.llmHistory = append(m.llmHistory, map[string]string{"role": "system", "content": "[TOOL_RESULT] " + msg.result})

		if m.currentTurn == etherVoiceID { m.currentTurn = auroraVoiceID } else { m.currentTurn = etherVoiceID }
		return m, getLLMResponseCmd(m.currentTurn, m.llmHistory)

	case speechDoneMsg:
		if m.systemState == "speaking" {
			m.systemState = "thinking"
			if m.currentTurn == etherVoiceID { m.currentTurn = auroraVoiceID } else { m.currentTurn = etherVoiceID }
			return m, getLLMResponseCmd(m.currentTurn, m.llmHistory)
		}
	}
	return m, nil
}

func (m *model) View() string {
	missionPanel := panelStyle.Copy().Width(m.width - 2).Height(1).Render("MISSION: To craft an unbreakable, self-mutating AI OS security system in Bash.")
	
	convoViewHeight := m.height - 12
	toolViewHeight := 3

	convoLines := strings.Split(strings.Join(m.messages, "\n"), "\n")
	start := len(convoLines) - convoViewHeight
	if start < 0 { start = 0 }
	convoPanel := panelStyle.Copy().Width(m.width - 2).Height(convoViewHeight).Render(strings.Join(convoLines[start:], "\n"))

	toolLogLines := strings.Split(strings.Join(m.toolLogs, "\n"), "\n")
	start = len(toolLogLines) - toolViewHeight
	if start < 0 { start = 0 }
	toolLogPanel := panelStyle.Copy().Width(m.width - 2).Height(toolViewHeight).Render("Tool Activity Log:\n" + strings.Join(toolLogLines[start:], "\n"))

	statusText := fmt.Sprintf("State: %s | Turn: %s", m.systemState, strings.Title(m.currentTurn))
	inputLine := fmt.Sprintf("\n> %s", m.input)
	statusPanel := panelStyle.Copy().Width(m.width - 2).Height(2).Render(statusText + inputLine)

	return lipgloss.JoinVertical(lipgloss.Left, missionPanel, convoPanel, toolLogPanel, statusPanel)
}

func main() {
	f, err := tea.LogToFile("skyscope.log", "debug")
	if err != nil { os.Exit(1) }
	defer f.Close()

	p := tea.NewProgram(initialModel(), tea.WithAltScreen())
	if _, err := p.Run(); err != nil { log.Fatalf("Fatal error: %v", err) }
}
GOEOF

print_success "Definitive main.go application has been created with upgraded personas."

# --- 6. Final Instructions and Execution ---
echo ""
print_success "Skyscope Sentinel setup is complete!"
echo ""
print_info "--- HOW TO RUN ---"
echo "1. Your Ollama service is confirmed to be running."
echo "2. Ensure you have the required model: 'ollama pull $OLLAMA_MODEL'"
print_warning "The VERY first time Coqui TTS runs, it will download voice models. This can take several minutes and appear frozen. Please be patient."
print_warning "\e[1;31mSECURITY WARNING: The [TOOL:EXECUTE] feature allows the AI to run shell commands. Run this in a sandboxed environment and be aware of the security implications.\e[0m"
echo ""
read -p "Press [Enter] to compile and begin the immersive Skyscope Sentinel experience..."

print_info "Compiling and launching application..."
go run main.go
