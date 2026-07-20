package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

// modelChoice describes a pre-configured model the user can pick from.
type modelChoice struct {
	Name        string // display name
	HuggingFace string // HF repo
	Runtime     string // "mlx" or "transformers"
	Description string // short one-liner
	MacOnly     bool   // true for MLX models
	Packages    string // required pip packages
}

var modelCatalog = []modelChoice{
	{
		Name:        "Llama 3.2 3B Instruct (MLX, 4-bit) — recommended for Apple Silicon",
		HuggingFace: "mlx-community/Llama-3.2-3B-Instruct-4bit",
		Runtime:     "mlx",
		Description: "Small, fast, great quality. ~2 GB. Best for M1/M2/M3 Macs.",
		MacOnly:     true,
		Packages:    "mlx-lm huggingface-hub",
	},
	{
		Name:        "Llama 3.2 1B Instruct (MLX, 4-bit) — lightweight for Apple Silicon",
		HuggingFace: "mlx-community/Llama-3.2-1B-Instruct-4bit",
		Runtime:     "mlx",
		Description: "Tiny, very fast. ~1 GB. Slightly lower quality but runs anywhere.",
		MacOnly:     true,
		Packages:    "mlx-lm huggingface-hub",
	},
	{
		Name:        "Llama 3.1 8B Instruct (MLX, 4-bit) — best quality for Apple Silicon",
		HuggingFace: "mlx-community/Meta-Llama-3.1-8B-Instruct-4bit",
		Runtime:     "mlx",
		Description: "Highest quality carriers. ~5 GB. Needs 8 GB+ RAM.",
		MacOnly:     true,
		Packages:    "mlx-lm huggingface-hub",
	},
	{
		Name:        "GPT-2 (Transformers) — works everywhere, no GPU needed",
		HuggingFace: "openai-community/gpt2",
		Runtime:     "transformers",
		Description: "Small classic model. ~500 MB. Works on any system with Python.",
		MacOnly:     false,
		Packages:    "torch transformers huggingface-hub",
	},
	{
		Name:        "GPT-2 Medium (Transformers) — better quality, still CPU-friendly",
		HuggingFace: "openai-community/gpt2-medium",
		Runtime:     "transformers",
		Description: "Better text quality. ~1.5 GB. CPU or GPU.",
		MacOnly:     false,
		Packages:    "torch transformers huggingface-hub",
	},
}

// defaultPromptForModel returns a reasonable default prompt for the given runtime.
func defaultPromptForModel(m modelChoice) string {
	if strings.Contains(strings.ToLower(m.HuggingFace), "llama") {
		return "<|begin_of_text|><|start_header_id|>system<|end_header_id|>\n\nWrite only one casual text to a friend. Sound like real chatting: contractions, half-thoughts, and everyday slang are fine. Continue the conversation cohesively from what was just said; each sentence should follow naturally. Ask at most one question. Vary openings; avoid stacking I just / I thought starters. Never invent people, add labels, or mention instructions or hidden data.<|eot_id|><|start_header_id|>user<|end_header_id|>\n\nok wait that dog skateboard clip is still living rent free in my head<|eot_id|><|start_header_id|>assistant<|end_header_id|>\n\nyeah the wipeout was unreal. i keep laughing about it at random times<|eot_id|><|start_header_id|>user<|end_header_id|>\n\nalso i need to return those headphones before the window closes<|eot_id|><|start_header_id|>assistant<|end_header_id|>\n\n"
	}
	return "anyway so earlier today "
}

// defaultChainSystemForModel returns a reasonable default chain_system prompt.
func defaultChainSystemForModel(m modelChoice) string {
	if strings.Contains(strings.ToLower(m.HuggingFace), "llama") {
		return "Write like you are casually texting a friend. Sound natural and a bit messy: contractions, half-thoughts, and everyday slang are fine. Continue the ongoing chat cohesively and naturally from the prior messages — react, answer, or build on what was said. Everyday topics are broad (shows, games, food, errands, weird observations, weekend plans, random opinions, pets, shopping, whatever), but do not jump to an unrelated subject unless the chat just started. Prefer 2 to 5 sentences with a concrete detail or two; sentences should flow as one bubble. Vary openings; do not start with I just / I was just / I thought / So I. If sending right after your own prior message, keep going with a new beat — do not rephrase it or reuse its opener. At most one question. No lists, labels, sign-offs, or repetition. Output only the message text."
	}
	return ""
}

func runSetupWizard(in io.Reader, out, errOut io.Writer) error {
	scanner := bufio.NewScanner(in)

	fmt.Fprintln(out, "  ┌─────────────────────────────────────┐")
	fmt.Fprintln(out, "  │          ⚙️  Setup Wizard            │")
	fmt.Fprintln(out, "  └─────────────────────────────────────┘")
	fmt.Fprintln(out)

	// --- Step 1: Python path ---
	fmt.Fprintln(out, "  Step 1/3: Python interpreter")
	fmt.Fprintln(out, "  Conversation Stenography uses a local AI model to generate cover text.")
	fmt.Fprintln(out, "  It needs Python with either 'mlx-lm' (Apple Silicon)")
	fmt.Fprintln(out, "  or 'torch + transformers' (any system).")
	fmt.Fprintln(out)

	pythonPath := ""
	candidates := detectAllPythons()
	if len(candidates) > 0 {
		fmt.Fprintln(out, "  Found Python installations:")
		fmt.Fprintln(out)
		for i, c := range candidates {
			marker := "  "
			if i == 0 {
				marker = "→ "
			}
			fmt.Fprintf(out, "  %s[%d] %s\n", marker, i+1, c.path)
			if len(c.available) > 0 {
				fmt.Fprintf(out, "       ✅ Has: %s\n", strings.Join(c.available, ", "))
			}
			if len(c.missing) > 0 {
				fmt.Fprintf(out, "       ❌ Missing: %s\n", strings.Join(c.missing, ", "))
			}
			fmt.Fprintln(out)
		}
		fmt.Fprintf(out, "  Pick one [1-%d, default 1], or type a path: ", len(candidates))
		if scanner.Scan() {
			text := strings.TrimSpace(scanner.Text())
			if text == "" || text == "1" {
				pythonPath = candidates[0].path
			} else if n, err := strconv.Atoi(text); err == nil && n >= 1 && n <= len(candidates) {
				pythonPath = candidates[n-1].path
			} else if text != "" {
				pythonPath = text
			}
		}
		if pythonPath == "" {
			pythonPath = candidates[0].path
		}
	} else {
		fmt.Fprintln(out, "  Could not find Python automatically.")
		fmt.Fprint(out, "  Enter path to your Python interpreter: ")
		if !scanner.Scan() {
			return scanner.Err()
		}
		pythonPath = strings.TrimSpace(scanner.Text())
		if pythonPath == "" {
			pythonPath = "python3"
		}
	}
	if err := validatePython(pythonPath); err != nil {
		return err
	}
	fmt.Fprintf(out, "\n  Using: %s\n\n", pythonPath)

	// --- Step 2: Choose model ---
	fmt.Fprintln(out, "  Step 2/3: Choose a language model")
	fmt.Fprintln(out, "  The model generates innocent-looking cover text that")
	fmt.Fprintln(out, "  hides your encrypted messages. Pick one to download:")
	fmt.Fprintln(out)

	isAppleSilicon := runtime.GOOS == "darwin" && runtime.GOARCH == "arm64"
	var available []modelChoice
	for _, m := range modelCatalog {
		if m.MacOnly && !isAppleSilicon {
			continue
		}
		available = append(available, m)
	}
	for i, m := range available {
		marker := "  "
		if i == 0 {
			marker = "→ "
		}
		fmt.Fprintf(out, "  %s[%d] %s\n", marker, i+1, m.Name)
		fmt.Fprintf(out, "       %s\n\n", m.Description)
	}
	fmt.Fprintf(out, "  Pick a model [1-%d, default 1]: ", len(available))
	choiceIdx := 0
	if scanner.Scan() {
		text := strings.TrimSpace(scanner.Text())
		if text != "" {
			n, err := strconv.Atoi(text)
			if err != nil || n < 1 || n > len(available) {
				fmt.Fprintln(errOut, "  Invalid choice, using default (1).")
			} else {
				choiceIdx = n - 1
			}
		}
	}
	chosen := available[choiceIdx]
	fmt.Fprintf(out, "\n  Selected: %s\n", chosen.Name)
	fmt.Fprintf(out, "  Runtime:  %s\n\n", chosen.Runtime)

	// --- Step 2.5: Check & install required packages ---
	missing := checkMissingPackages(pythonPath, chosen)
	if len(missing) > 0 {
		fmt.Fprintf(out, "  Your Python is missing required packages: %s\n\n", strings.Join(missing, ", "))
		fmt.Fprintln(out, "  Install them now?")
		fmt.Fprintln(out, "  If this is a system Python, Conversation Stenography will create an isolated")
		fmt.Fprintln(out, "  .conversation-stenography-venv so it does not modify your global packages.")
		fmt.Fprintln(out)
		fmt.Fprint(out, "  Install? [Y/n]: ")

		doInstall := true
		if scanner.Scan() {
			answer := strings.TrimSpace(strings.ToLower(scanner.Text()))
			if answer == "n" || answer == "no" {
				doInstall = false
			}
		}

		if doInstall {
			fmt.Fprintln(out)
			fmt.Fprintln(out, "  📦 Installing packages...")
			fmt.Fprintln(out)
			var err error
			pythonPath, err = preparePythonEnvironment(pythonPath, out, errOut)
			if err != nil {
				return fmt.Errorf("prepare Python environment: %w", err)
			}
			if err := installPackages(pythonPath, missing, out, errOut); err != nil {
				fmt.Fprintf(errOut, "\n  ⚠ Installation failed: %v\n\n", err)
				fmt.Fprintln(out, "  You can install manually:")
				fmt.Fprintf(out, "    %s -m pip install %s\n\n", pythonPath, strings.Join(missing, " "))
				fmt.Fprintln(out, "  Then re-run: ./conversation-stenography setup")
				return fmt.Errorf("package installation failed: %w", err)
			}
			if stillMissing := checkMissingPackages(pythonPath, chosen); len(stillMissing) > 0 {
				return fmt.Errorf("installation finished but packages still cannot be imported: %s", strings.Join(stillMissing, ", "))
			}
			fmt.Fprintln(out, "  ✅ Packages installed successfully!")
			fmt.Fprintln(out)
		} else {
			fmt.Fprintln(out)
			fmt.Fprintln(out, "  You'll need to install them manually before Conversation Stenography can work:")
			fmt.Fprintf(out, "    %s -m pip install %s\n\n", pythonPath, strings.Join(missing, " "))
			fmt.Fprintln(out, "  Then re-run: ./conversation-stenography setup")
			return fmt.Errorf("required packages not installed: %s", strings.Join(missing, ", "))
		}
	}

	// --- Step 3: Download ---
	fmt.Fprintln(out, "  Step 3/3: Download the model")
	fmt.Fprintf(out, "  Downloading %s...\n", chosen.HuggingFace)
	fmt.Fprintln(out, "  (This may take a few minutes on first run.)")
	fmt.Fprintln(out)

	modelPath, revision, err := downloadModel(pythonPath, chosen, out)
	if err != nil {
		fmt.Fprintln(errOut)
		fmt.Fprintln(errOut, "  ⚠ Model download failed:", err)
		fmt.Fprintln(errOut)
		fmt.Fprintln(errOut, "  You can download it manually:")
		fmt.Fprintf(errOut, "    hf download %s\n\n", chosen.HuggingFace)

		// Ask for manual path
		fmt.Fprint(out, "  Enter local model path (or press Enter to retry later): ")
		if !scanner.Scan() {
			return fmt.Errorf("model download failed: %w", err)
		}
		modelPath = strings.TrimSpace(scanner.Text())
		if modelPath == "" {
			return fmt.Errorf("model download failed: %w", err)
		}
		if pathErr := validateModelPath(modelPath); pathErr != nil {
			return fmt.Errorf("invalid local model path: %w", pathErr)
		}
		revision = "main"
	}

	// --- Build config ---
	topN := 256
	if chosen.Runtime == "transformers" {
		topN = 8
	}

	cfg := localGenerativeConfig{
		Runtime:           chosen.Runtime,
		Python:            pythonPath,
		Model:             modelPath,
		Revision:          revision,
		Prompt:            defaultPromptForModel(chosen),
		ChainSystem:       defaultChainSystemForModel(chosen),
		TopN:              topN,
		Coding:            "arithmetic",
		Temperature:       1.0,
		FinishTokens:      32,
		StrictStyle:       true,
		CandidatePool:     8,
		RefreshSentences:  false,
		CarrierTrials:     2,
		NaturalnessSlack:  0.35,
		SemanticJudge:     false,
		SemanticThreshold: -6.0,
		LengthBias:        0.1,
		Secure:            true,
		Conversation:      "default-chat",
		Direction:         "sender-to-receiver",
	}

	configPath := resolveSupportFile("conversation-stenography.local.json")
	if err := saveLocalGenerativeConfig(configPath, cfg); err != nil {
		return err
	}

	fmt.Fprintln(out, "  ┌─────────────────────────────────────┐")
	fmt.Fprintln(out, "  │          ✅  Setup complete!         │")
	fmt.Fprintln(out, "  └─────────────────────────────────────┘")
	fmt.Fprintln(out)
	fmt.Fprintf(out, "  Config saved to: %s\n", configPath)
	fmt.Fprintln(out, "  Model: "+chosen.Name)
	fmt.Fprintln(out)
	fmt.Fprintln(out, "  To start chatting, just run:")
	fmt.Fprintln(out, "    ./conversation-stenography")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "  To test both sides on this device:")
	fmt.Fprintln(out, "    ./conversation-stenography simulate")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "  Or to re-run setup:")
	fmt.Fprintln(out, "    ./conversation-stenography setup")
	fmt.Fprintln(out)

	return nil
}

// pythonCandidate holds a discovered Python with its package availability.
type pythonCandidate struct {
	path      string
	available []string // packages that are importable
	missing   []string // packages that are not importable
}

func validatePython(python string) error {
	cmd := exec.Command(python, "-c", "import sys; print('%d.%d' % sys.version_info[:2]); raise SystemExit(sys.version_info < (3, 9))")
	output, err := cmd.CombinedOutput()
	if err != nil {
		detail := strings.TrimSpace(string(output))
		if detail != "" {
			return fmt.Errorf("Python %q is unavailable or older than 3.9 (%s): %w", python, detail, err)
		}
		return fmt.Errorf("Python %q is unavailable or older than 3.9: %w", python, err)
	}
	return nil
}

// detectAllPythons finds Python interpreters and checks which ML packages they have.
func detectAllPythons() []pythonCandidate {
	// Collect unique python paths — use absolute paths for dedup but do NOT
	// resolve symlinks because venv pythons are symlinks to the system binary
	// yet have completely different package search paths.
	seen := map[string]bool{}
	var paths []string

	addPath := func(p string) {
		abs, err := filepath.Abs(p)
		if err != nil {
			abs = p
		}
		if seen[abs] {
			return
		}
		seen[abs] = true
		paths = append(paths, abs)
	}

	// 1. System python
	for _, name := range []string{"python3", "python"} {
		if p, err := exec.LookPath(name); err == nil {
			addPath(p)
		}
	}

	// 2. Common venv locations relative to current directory and home
	cwd, _ := os.Getwd()
	home, _ := os.UserHomeDir()
	searchDirs := []string{cwd}
	if home != "" {
		searchDirs = append(searchDirs, home)
	}
	// Also check parent of cwd and siblings (common project layouts)
	if parent := filepath.Dir(cwd); parent != cwd {
		searchDirs = append(searchDirs, parent)
		if entries, err := os.ReadDir(parent); err == nil {
			for _, e := range entries {
				if e.IsDir() {
					searchDirs = append(searchDirs, filepath.Join(parent, e.Name()))
				}
			}
		}
	}

	venvNames := []string{"venv", ".venv", "env", ".env"}
	for _, dir := range searchDirs {
		for _, vname := range venvNames {
			p := filepath.Join(dir, vname, "bin", "python")
			if _, err := os.Stat(p); err == nil {
				addPath(p)
			}
		}
	}

	// 3. Probe each Python for packages
	packagesToCheck := []string{"mlx_lm", "torch", "transformers", "huggingface_hub"}
	var results []pythonCandidate
	for _, p := range paths {
		avail, miss := probePackages(p, packagesToCheck)
		results = append(results, pythonCandidate{path: p, available: avail, missing: miss})
	}

	// Sort: pythons with more available packages first
	for i := 1; i < len(results); i++ {
		for j := i; j > 0 && len(results[j].available) > len(results[j-1].available); j-- {
			results[j], results[j-1] = results[j-1], results[j]
		}
	}

	return results
}

// probePackages checks which Python packages are importable.
func probePackages(python string, packages []string) (available, missing []string) {
	script := `import importlib.util, sys
for name in sys.argv[1:]:
    if importlib.util.find_spec(name) is not None:
        print(name)
`
	output, err := exec.Command(python, append([]string{"-c", script}, packages...)...).Output()
	if err != nil {
		return nil, append([]string(nil), packages...)
	}
	found := make(map[string]bool, len(packages))
	for _, pkg := range strings.Fields(string(output)) {
		found[pkg] = true
	}
	for _, pkg := range packages {
		if found[pkg] {
			available = append(available, pkg)
		} else {
			missing = append(missing, pkg)
		}
	}
	return
}

// checkMissingPackages returns packages required for the chosen model that
// the given Python interpreter doesn't have.
func checkMissingPackages(python string, model modelChoice) []string {
	// Always need huggingface_hub for downloading
	required := []string{"huggingface_hub"}

	switch model.Runtime {
	case "mlx":
		required = append(required, "mlx_lm")
	case "transformers":
		required = append(required, "torch", "transformers")
	}

	available, _ := probePackages(python, required)
	found := make(map[string]bool, len(available))
	for _, pkg := range available {
		found[pkg] = true
	}
	var missing []string
	for _, pkg := range required {
		if !found[pkg] {
			// Convert import name back to pip name
			pipName := strings.ReplaceAll(pkg, "_", "-")
			missing = append(missing, pipName)
		}
	}
	return missing
}

// installPackages runs pip install for the given packages, streaming output
// so the user can see progress.
func installPackages(python string, packages []string, out, errOut io.Writer) error {
	if len(packages) == 0 {
		return nil
	}
	if err := exec.Command(python, "-m", "pip", "--version").Run(); err != nil {
		fmt.Fprintln(out, "  pip is not available; trying Python's ensurepip...")
		bootstrap := exec.Command(python, "-m", "ensurepip", "--upgrade")
		bootstrap.Stdout = out
		bootstrap.Stderr = errOut
		if bootstrapErr := bootstrap.Run(); bootstrapErr != nil {
			return fmt.Errorf("pip is unavailable and ensurepip failed: %w", bootstrapErr)
		}
	}

	args := append([]string{"-m", "pip", "install", "--upgrade"}, packages...)
	cmd := exec.Command(python, args...)
	cmd.Stdout = out
	cmd.Stderr = errOut
	if err := cmd.Run(); err != nil {
		return fmt.Errorf(
			"%s -m pip install failed: %w (if this Python is externally managed, select or create a virtual environment and rerun setup)",
			python, err,
		)
	}
	return nil
}

func preparePythonEnvironment(python string, out, errOut io.Writer) (string, error) {
	cmd := exec.Command(python, "-c", "import sys; print(int(sys.prefix != sys.base_prefix))")
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("run selected Python %q: %w", python, err)
	}
	if strings.TrimSpace(string(output)) == "1" {
		return python, nil
	}

	configDir := filepath.Dir(resolveSupportFile("conversation-stenography.local.json"))
	venvDir := filepath.Join(configDir, ".conversation-stenography-venv")
	venvPython := filepath.Join(venvDir, "bin", "python")
	if runtime.GOOS == "windows" {
		venvPython = filepath.Join(venvDir, "Scripts", "python.exe")
	}
	if _, err := os.Stat(venvPython); errors.Is(err, os.ErrNotExist) {
		fmt.Fprintf(out, "  Creating isolated environment at %s...\n", venvDir)
		create := exec.Command(python, "-m", "venv", venvDir)
		create.Stdout = out
		create.Stderr = errOut
		if err := create.Run(); err != nil {
			return "", fmt.Errorf("create virtual environment (your Python may need its venv package): %w", err)
		}
	}
	if err := exec.Command(venvPython, "-c", "import sys").Run(); err != nil {
		return "", fmt.Errorf("created virtual environment is unusable at %s: %w", venvPython, err)
	}
	fmt.Fprintf(out, "  Using isolated Python: %s\n", venvPython)
	return venvPython, nil
}

const downloadPathMarker = "CONVERSATION_STENOGRAPHY_MODEL_PATH="

// downloadModel uses the huggingface_hub installed in the selected interpreter.
// This avoids depending on a console script being on PATH. Only a marked final
// line is parsed, so progress messages and ANSI color codes can never corrupt
// the path saved in the config.
func downloadModel(python string, m modelChoice, out io.Writer) (modelPath, revision string, err error) {
	fmt.Fprintf(out, "  Fetching model files (this can take several minutes)...\n")
	script := `import os, sys
try:
    from huggingface_hub import snapshot_download
    path = snapshot_download(repo_id=sys.argv[1])
except Exception as exc:
    print("Model download failed: " + str(exc), file=sys.stderr)
    raise SystemExit(1)
print("` + downloadPathMarker + `" + os.path.realpath(path))
`
	cmd := exec.Command(python, "-c", script, m.HuggingFace)
	cmd.Stderr = out
	output, err := cmd.Output()
	if err != nil {
		return "", "", fmt.Errorf("download %s with %s: %w", m.HuggingFace, python, err)
	}

	for _, line := range strings.Split(string(output), "\n") {
		if strings.HasPrefix(line, downloadPathMarker) {
			modelPath = strings.TrimSpace(strings.TrimPrefix(line, downloadPathMarker))
		}
	}
	if err := validateModelPath(modelPath); err != nil {
		return "", "", err
	}
	fmt.Fprintln(out, "  Download complete!")

	revision = filepath.Base(modelPath)
	if revision == "" || revision == "." || revision == "/" {
		revision = "main"
	}
	return modelPath, revision, nil
}

func validateModelPath(path string) error {
	if path == "" {
		return errors.New("download completed without returning a model path")
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("download returned unusable model path %q: %w", path, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("download returned model path %q, which is not a directory", path)
	}
	for _, name := range []string{"config.json", "tokenizer.json", "tokenizer_config.json"} {
		if _, err := os.Stat(filepath.Join(path, name)); err == nil {
			return nil
		}
	}
	return fmt.Errorf("downloaded directory %q does not look like a Hugging Face model", path)
}

func saveLocalGenerativeConfig(path string, cfg localGenerativeConfig) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("create config directory %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".conversation-stenography-config-*")
	if err != nil {
		return fmt.Errorf("create temporary config: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return fmt.Errorf("secure temporary config: %w", err)
	}
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		return fmt.Errorf("write temporary config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temporary config: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replace config %s: %w", path, err)
	}
	return nil
}
