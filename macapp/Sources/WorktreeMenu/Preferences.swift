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

    private var editorPopUp: NSPopUpButton?
    private var customTemplateField: NSTextField?
    private var wtPathField: NSTextField?
    private var refreshIntervalField: NSTextField?

    init(defaults: UserDefaults = .standard, workspace: NSWorkspace = .shared) {
        self.defaults = defaults
        self.installedEditors = InstalledEditors.detect(workspace: workspace)

        let window = NSWindow(
            contentRect: NSRect(x: 0, y: 0, width: 440, height: 230),
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
}
