# Process Manager

**Process Manager** is a terminal-based tool written in Go for managing processes. The application allows you to launch, monitor, and view logs of processes in real time through a convenient TUI (Text User Interface) built with [Bubble Tea](https://github.com/charmbracelet/bubbletea).

## Features

- Automatically launches processes from a configuration file on startup.
- Add new processes during runtime.
- Automatic Restart: If a process unexpectedly terminates or completes normally, it will be restarted automatically based on its configuration.
- View logs of running processes with scrolling and force-kill functionality.
- Limits log size per process (up to 1000 lines) to prevent excessive memory usage.
- Automatically backs up and resets the configuration file if it becomes unreadable.

## Requirements

- [Go](https://golang.org/dl/) 1.16 or newer.
- Supported platforms: Windows, Linux, macOS.
- Go module dependencies:
  - [Bubble Tea](https://github.com/charmbracelet/bubbletea)
  - [Bubbles](https://github.com/charmbracelet/bubbles)
  - [Lip Gloss](https://github.com/charmbracelet/lipgloss)

## Installation

1. **Clone the repository:**

   ```bash
   git clone https://github.com/yourusername/process-manager.git
   cd process-manager
   ```

2. **Install dependencies:**

   Make sure you have Go installed. Then run:

   ```bash
   go mod download
   ```

3. **Build the project:**

   ```bash
   go build -o process-manager
   ```

   This will compile the executable `process-manager` in the current directory.

## Usage

1. **Run the application:**

   ```bash
   ./process-manager
   ```

   On the first run, a configuration file named `process_manager_config.json` will be created in the same directory as the executable.

2. **Configuration File:**

   The `process_manager_config.json` file holds a list of processes to be launched automatically. Its format:

   ```json
   {
     "processes": [
       {
         "cmd_line": "command_1",
         "auto_restart": true
       },
       {
         "cmd_line": "command_2",
         "auto_restart": false
       }
     ]
   }
   ```

   - **cmd_line:** The command line to execute the process.
   - **auto_restart:** If set to `true`, the process will automatically restart upon termination.

   **Note:**  
   If the configuration file cannot be read (due to corruption or invalid format), the application will rename the existing `process_manager_config.json` to `process_manager_config.json.old` and create a new default configuration file.

3. **Application Interface:**

   - **Process List:** Upon launch, the interface displays a list of running processes. The last item in the list allows you to create a new process.
   - **Adding a Process:** Select the option to create a new process, enter the command, and press `Enter`. You can toggle the auto-restart option by pressing `Tab`.
   - **Viewing Logs:** Select an existing process from the list and press `Enter` to view its logs. In log view mode:
     - Use the `↑/↓` arrow keys or `k/j` to scroll.
     - Press `ctrl+k` to forcefully kill the process.
     - Press `Esc` to return to the process list.
   - **Exit:** Press `q` or `ctrl+c` to quit the application. All unfinished processes will be terminated gracefully.

## Logs and Limitations

- Each process's logs are stored in memory up to 1000 lines. Older lines are removed as new lines are added to maintain this limit.

## Notes

- On Windows, UTF-8 encoding is set for proper character display (the code runs `chcp 65001`).
- Ensure your terminal supports [Alternate Screen Mode](https://en.wikipedia.org/wiki/Alternate_screen_buffer) for optimal display.

## License

This project is licensed under the [MIT License](LICENSE).