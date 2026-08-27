import AppKit
import WorktreeMenuCore

/// The `UserDefaults` keys this window reads and writes.
/// `WtClient.binaryPathDefaultsKey` (`WorktreeMenuCore`) is the `wt` path
/// override and is reused here rather than given a second key, per
/// PLAN.md.
enum PreferencesKeys {
    /// The id of the chosen editor: one of `EditorLauncher.builtIns`' ids,
    /// or `EditorLauncher.customEditorID` for the free-text template.
    static let editorID = "WTEditorID"
    /// The free-text template used when `editorID` is
    /// `EditorLauncher.customEditorID`.
    static let customEditorTemplate = "WTCustomEditorTemplate"
    /// The menu's periodic refresh interval, in seconds.
    static let refreshIntervalSeconds = "WTRefreshIntervalSeconds"

    /// What `AppDelegate`'s refresh timer uses when nothing is stored yet
    /// — the interval phase 1 hardcoded.
    static let defaultRefreshIntervalSeconds: TimeInterval = 60
}

/// Detects which of `EditorLauncher.builtIns` has an application
/// registered for its scheme. This is the AppKit half of "detect
/// installed editors" (PLAN.md); the policy of which schemes exist and
/// what their templates look like stays in `WorktreeMenuCore`, along with
/// `EditorLauncher.resolveTemplate`, which combines this list with the
/// stored preference.
enum InstalledEditors {
    static func detect(workspace: NSWorkspace = .shared) -> [EditorDefinition] {
        EditorLauncher.builtIns.filter { definition in
            guard let probeURL = URL(string: "\(definition.scheme)://") else { return false }
            return workspace.urlForApplication(toOpen: probeURL) != nil
        }
    }
}

/// The preferences window: the editor choice, the `wt` path override, and
/// the refresh interval. Every control reads from and writes straight to
/// `UserDefaults` (PLAN.md) — there is no separate model to keep in sync
/// with it.
@MainActor
final class PreferencesWindowController: NSWindowController {
    private let defaults: UserDefaults
    private let installedEditors: [EditorDefinition]
    private let launchAtLogin: LaunchAtLoginRegistering

    private var editorPopUp: NSPopUpButton?
    private var customTemplateField: NSTextField?
    private var wtPathField: NSTextField?
    private var refreshIntervalField: NSTextField?
    private var launchAtLoginCheckbox: NSButton?
    private var launchAtLoginStatusLabel: NSTextField?

    init(
        defaults: UserDefaults = .standard,
        workspace: NSWorkspace = .shared,
        launchAtLogin: LaunchAtLoginRegistering = SMAppServiceLaunchAtLogin()
    ) {
        self.defaults = defaults
        self.installedEditors = InstalledEditors.detect(workspace: workspace)
        self.launchAtLogin = launchAtLogin

        let window = NSWindow(
            contentRect: NSRect(x: 0, y: 0, width: 440, height: 270),
            styleMask: [.titled, .closable],
            backing: .buffered,
            defer: false
        )
        window.title = "Worktree Menu Preferences"
        window.isReleasedWhenClosed = false
        window.center()

        super.init(window: window)
        buildContent()
        loadFromDefaults()
    }

    @available(*, unavailable)
    required init?(coder: NSCoder) {
        fatalError("PreferencesWindowController does not support NSCoder")
    }

    private func buildContent() {
        guard let window else { return }

        let editorLabel = NSTextField(labelWithString: "Open worktrees with:")
        let editorPopUp = NSPopUpButton(frame: .zero, pullsDown: false)
        for editor in installedEditors {
            let item = NSMenuItem(title: editor.displayName, action: nil, keyEquivalent: "")
            item.representedObject = editor.id
            editorPopUp.menu?.addItem(item)
        }
        let customItem = NSMenuItem(title: "Custom template…", action: nil, keyEquivalent: "")
        customItem.representedObject = EditorLauncher.customEditorID
        editorPopUp.menu?.addItem(customItem)
        editorPopUp.target = self
        editorPopUp.action = #selector(editorChanged(_:))
        self.editorPopUp = editorPopUp

        let customLabel = NSTextField(labelWithString: "Custom template (use {path}):")
        let customTemplateField = NSTextField(string: "")
        customTemplateField.placeholderString = "myeditor://open?file={path}"
        customTemplateField.target = self
        customTemplateField.action = #selector(customTemplateChanged(_:))
        self.customTemplateField = customTemplateField

        let wtPathLabel = NSTextField(labelWithString: "wt binary path (blank for auto-detect):")
        let wtPathField = NSTextField(string: "")
        wtPathField.placeholderString = "~/.local/bin/wt"
        wtPathField.target = self
        wtPathField.action = #selector(wtPathChanged(_:))
        self.wtPathField = wtPathField

        let refreshLabel = NSTextField(labelWithString: "Refresh interval (seconds):")
        let refreshIntervalField = NSTextField(string: "")
        refreshIntervalField.target = self
        refreshIntervalField.action = #selector(refreshIntervalChanged(_:))
        self.refreshIntervalField = refreshIntervalField

        let launchAtLoginCheckbox = NSButton(
            checkboxWithTitle: "Launch at login",
            target: self,
            action: #selector(launchAtLoginChanged(_:))
        )
        self.launchAtLoginCheckbox = launchAtLoginCheckbox

        let launchAtLoginStatusLabel = NSTextField(labelWithString: "")
        launchAtLoginStatusLabel.font = .systemFont(ofSize: NSFont.smallSystemFontSize)
        launchAtLoginStatusLabel.textColor = .secondaryLabelColor
        launchAtLoginStatusLabel.lineBreakMode = .byWordWrapping
        launchAtLoginStatusLabel.maximumNumberOfLines = 2
        launchAtLoginStatusLabel.preferredMaxLayoutWidth = 380
        self.launchAtLoginStatusLabel = launchAtLoginStatusLabel

        let launchAtLoginStack = NSStackView(views: [launchAtLoginCheckbox, launchAtLoginStatusLabel])
        launchAtLoginStack.orientation = .vertical
        launchAtLoginStack.alignment = .leading
        launchAtLoginStack.spacing = 2

        func row(_ label: NSView, _ control: NSView) -> NSStackView {
            let stack = NSStackView(views: [label, control])
            stack.orientation = .horizontal
            stack.alignment = .firstBaseline
            stack.spacing = 8
            control.setContentHuggingPriority(.defaultLow, for: .horizontal)
            control.widthAnchor.constraint(equalToConstant: 220).isActive = true
            return stack
        }

        let stack = NSStackView(views: [
            row(editorLabel, editorPopUp),
            row(customLabel, customTemplateField),
            row(wtPathLabel, wtPathField),
            row(refreshLabel, refreshIntervalField),
            launchAtLoginStack,
        ])
        stack.orientation = .vertical
        stack.alignment = .leading
        stack.spacing = 14
        stack.edgeInsets = NSEdgeInsets(top: 20, left: 20, bottom: 20, right: 20)
        stack.translatesAutoresizingMaskIntoConstraints = false

        let contentView = NSView()
        contentView.addSubview(stack)
        NSLayoutConstraint.activate([
            stack.topAnchor.constraint(equalTo: contentView.topAnchor),
            stack.leadingAnchor.constraint(equalTo: contentView.leadingAnchor),
            stack.trailingAnchor.constraint(lessThanOrEqualTo: contentView.trailingAnchor),
            stack.bottomAnchor.constraint(lessThanOrEqualTo: contentView.bottomAnchor),
        ])
        window.contentView = contentView
    }

    private func loadFromDefaults() {
        let selectedID = defaults.string(forKey: PreferencesKeys.editorID)
        if let editorPopUp {
            if let selectedID, let item = editorPopUp.menu?.items.first(where: { $0.representedObject as? String == selectedID }) {
                editorPopUp.select(item)
            } else if let firstItem = editorPopUp.menu?.items.first {
                editorPopUp.select(firstItem)
            }
        }
        customTemplateField?.stringValue = defaults.string(forKey: PreferencesKeys.customEditorTemplate) ?? ""
        customTemplateField?.isEnabled = (selectedID == EditorLauncher.customEditorID)

        wtPathField?.stringValue = defaults.string(forKey: WtClient.binaryPathDefaultsKey) ?? ""

        let interval = defaults.object(forKey: PreferencesKeys.refreshIntervalSeconds) as? Double
            ?? PreferencesKeys.defaultRefreshIntervalSeconds
        refreshIntervalField?.stringValue = String(Int(interval))

        refreshLaunchAtLoginState()
    }

    /// Reads `SMAppService.mainApp`'s current status (through the
    /// injected `launchAtLogin`) and reflects it in the checkbox and the
    /// status label below it. Called on open and after every toggle
    /// attempt, so a denial in System Settings or a throw from
    /// `register()`/`unregister()` is always shown as the true state
    /// rather than whatever the checkbox optimistically flipped to.
    private func refreshLaunchAtLoginState() {
        let status = launchAtLogin.currentStatus
        launchAtLoginCheckbox?.state = (status == .enabled) ? .on : .off
        launchAtLoginCheckbox?.isEnabled = (status != .notFound)
        launchAtLoginStatusLabel?.stringValue = LaunchAtLoginPreference.statusDescription(status)
    }

    @objc private func editorChanged(_ sender: NSPopUpButton) {
        let id = sender.selectedItem?.representedObject as? String
        defaults.set(id, forKey: PreferencesKeys.editorID)
        customTemplateField?.isEnabled = (id == EditorLauncher.customEditorID)
    }

    @objc private func customTemplateChanged(_ sender: NSTextField) {
        defaults.set(sender.stringValue, forKey: PreferencesKeys.customEditorTemplate)
    }

    @objc private func wtPathChanged(_ sender: NSTextField) {
        defaults.set(sender.stringValue, forKey: WtClient.binaryPathDefaultsKey)
    }

    @objc private func refreshIntervalChanged(_ sender: NSTextField) {
        guard let value = Double(sender.stringValue), value > 0 else { return }
        defaults.set(value, forKey: PreferencesKeys.refreshIntervalSeconds)
    }

    /// Registers or unregisters with `SMAppService.mainApp` to match the
    /// checkbox the user just clicked. Both calls can throw — the user
    /// may have denied the request, or already removed the app from
    /// Login Items in System Settings — so the checkbox is never trusted
    /// on its own: `refreshLaunchAtLoginState` re-reads the real status
    /// afterwards and the checkbox settles there, not wherever the click
    /// left it (PLAN.md: "handle the failure case").
    @objc private func launchAtLoginChanged(_ sender: NSButton) {
        let wantsOn = (sender.state == .on)
        do {
            if wantsOn {
                try launchAtLogin.register()
            } else {
                try launchAtLogin.unregister()
            }
            refreshLaunchAtLoginState()
        } catch {
            // Leave the failure text in place rather than letting
            // refreshLaunchAtLoginState's generic status line overwrite
            // it; only the checkbox is resynced to the real status.
            let status = launchAtLogin.currentStatus
            launchAtLoginCheckbox?.state = (status == .enabled) ? .on : .off
            launchAtLoginStatusLabel?.stringValue = LaunchAtLoginPreference.failureDescription(
                togglingOn: wantsOn,
                error: error
            )
        }
    }
}
