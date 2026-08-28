import AppKit
import WorktreeMenuCore

/// The doctor report as a window rather than an alert.
///
/// An `NSAlert` puts the whole report in `informativeText`, which is one
/// unscrollable paragraph block: forty findings become a wall a person
/// cannot skim, cannot select part of, and cannot act on without retyping a
/// command into a terminal. This window renders `DoctorCore.report` — the
/// findings grouped by app, worst first — as a scrolling stack of rows,
/// each with its detail, its fix, and the two buttons that make the fix
/// something you do rather than something you read: copy the command, or
/// reveal the file it is about.
///
/// The layout decisions that matter — what a row says, what order the rows
/// come in, what the headline reads — are `DoctorCore`'s and are tested
/// there. This file is the thin AppKit step, the same split `MenuBuilder`
/// and the menu already use.
@MainActor
final class DoctorReportWindowController: NSWindowController {
    /// What the window is showing: a report, the error from a check that
    /// could not run, or the state before the first check completes.
    enum Content {
        /// A report, and — when the check that would have replaced it
        /// failed — why what is on screen is older than it looks. The app's
        /// refresh model is to show stale data and the reason it is stale
        /// together, and a window that quietly redisplays a report from ten
        /// minutes ago is the one place that rule could be broken silently.
        case report(DoctorResult, staleReason: String? = nil)
        case failed(String)
        case checking
    }

    /// Runs a fresh `wt doctor` for the "Check again" button. The window
    /// never shells out itself — the delegate owns the client and the
    /// refresh coordinator, and hands the result back through `show`.
    private let recheck: () -> Void

    private let headline = NSTextField(labelWithString: "")
    private let subhead = NSTextField(labelWithString: "")
    private let stack = NSStackView()
    private let scroll = NSScrollView()
    private var copyReportButton: NSButton?
    private var currentReportText = ""

    /// Section titles the reader has collapsed, kept across a re-check so a
    /// refresh does not reopen what they just closed.
    private var collapsed: Set<String> = []
    /// The notes section starts collapsed: what doctor could not check is
    /// never the reason anybody opened the window.
    private var notesCollapsed = true
    private var lastContent: Content = .checking

    init(recheck: @escaping () -> Void) {
        self.recheck = recheck
        let window = NSWindow(
            contentRect: NSRect(x: 0, y: 0, width: 640, height: 560),
            styleMask: [.titled, .closable, .miniaturizable, .resizable],
            backing: .buffered,
            defer: false
        )
        window.title = "Worktree Doctor"
        window.isReleasedWhenClosed = false
        window.minSize = NSSize(width: 460, height: 320)
        super.init(window: window)
        window.contentView = buildContentView()
        window.center()
    }

    @available(*, unavailable)
    required init?(coder: NSCoder) { fatalError("not loaded from a nib") }

    // MARK: the chrome

    private func buildContentView() -> NSView {
        let root = NSView()

        headline.font = .systemFont(ofSize: 17, weight: .semibold)
        headline.lineBreakMode = .byTruncatingTail
        subhead.font = .systemFont(ofSize: 11)
        subhead.textColor = .secondaryLabelColor
        subhead.lineBreakMode = .byTruncatingTail

        let recheckButton = NSButton(title: "Check again", target: self, action: #selector(recheckClicked))
        recheckButton.bezelStyle = .rounded

        let titles = NSStackView(views: [headline, subhead])
        titles.orientation = .vertical
        titles.alignment = .leading
        titles.spacing = 2

        let header = NSStackView(views: [titles, NSView(), recheckButton])
        header.orientation = .horizontal
        header.alignment = .centerY
        header.spacing = 12
        header.edgeInsets = NSEdgeInsets(top: 16, left: 20, bottom: 12, right: 20)
        titles.setContentHuggingPriority(.defaultLow, for: .horizontal)
        recheckButton.setContentHuggingPriority(.required, for: .horizontal)

        stack.orientation = .vertical
        // .width makes every arranged subview as wide as the stack, so the
        // wrapping labels inside wrap to the window instead of each picking
        // its own natural width.
        stack.alignment = .width
        stack.spacing = 0
        stack.edgeInsets = NSEdgeInsets(top: 0, left: 20, bottom: 16, right: 20)
        stack.translatesAutoresizingMaskIntoConstraints = false

        let clip = FlippedView()
        clip.translatesAutoresizingMaskIntoConstraints = false
        clip.addSubview(stack)
        NSLayoutConstraint.activate([
            stack.topAnchor.constraint(equalTo: clip.topAnchor),
            stack.leadingAnchor.constraint(equalTo: clip.leadingAnchor),
            stack.trailingAnchor.constraint(equalTo: clip.trailingAnchor),
            stack.bottomAnchor.constraint(equalTo: clip.bottomAnchor),
        ])

        scroll.hasVerticalScroller = true
        scroll.autohidesScrollers = true
        scroll.drawsBackground = false
        scroll.documentView = clip
        scroll.translatesAutoresizingMaskIntoConstraints = false
        // The document tracks the clip view's width so every label wraps to
        // the window rather than scrolling sideways.
        NSLayoutConstraint.activate([
            clip.widthAnchor.constraint(equalTo: scroll.contentView.widthAnchor)
        ])

        let copyButton = NSButton(title: "Copy report", target: self, action: #selector(copyReportClicked))
        copyButton.bezelStyle = .rounded
        copyReportButton = copyButton
        let doneButton = NSButton(title: "Done", target: self, action: #selector(doneClicked))
        doneButton.bezelStyle = .rounded
        doneButton.keyEquivalent = "\r"

        let footer = NSStackView(views: [NSView(), copyButton, doneButton])
        footer.orientation = .horizontal
        footer.spacing = 10
        footer.edgeInsets = NSEdgeInsets(top: 10, left: 20, bottom: 14, right: 20)

        let divider = separator()
        for view in [header, scroll, divider, footer] as [NSView] {
            view.translatesAutoresizingMaskIntoConstraints = false
            root.addSubview(view)
        }
        NSLayoutConstraint.activate([
            header.topAnchor.constraint(equalTo: root.topAnchor),
            header.leadingAnchor.constraint(equalTo: root.leadingAnchor),
            header.trailingAnchor.constraint(equalTo: root.trailingAnchor),

            scroll.topAnchor.constraint(equalTo: header.bottomAnchor),
            scroll.leadingAnchor.constraint(equalTo: root.leadingAnchor),
            scroll.trailingAnchor.constraint(equalTo: root.trailingAnchor),
            scroll.bottomAnchor.constraint(equalTo: divider.topAnchor),

            divider.leadingAnchor.constraint(equalTo: root.leadingAnchor),
            divider.trailingAnchor.constraint(equalTo: root.trailingAnchor),
            divider.bottomAnchor.constraint(equalTo: footer.topAnchor),

            footer.leadingAnchor.constraint(equalTo: root.leadingAnchor),
            footer.trailingAnchor.constraint(equalTo: root.trailingAnchor),
            footer.bottomAnchor.constraint(equalTo: root.bottomAnchor),
        ])
        return root
    }

    // MARK: showing a report

    /// Renders `content` and brings the window forward. Calling this again
    /// with a fresh result re-renders in place, which is what makes "Check
    /// again" and the background timer land in the open window rather than
    /// opening a second one.
    func show(_ content: Content) {
        lastContent = content
        render(content)
        scroll.contentView.scroll(to: .zero)
        NSApp.activate(ignoringOtherApps: true)
        showWindow(nil)
        window?.makeKeyAndOrderFront(nil)
    }

    /// Re-renders an already-open window from a fresh result, without
    /// stealing focus. The background doctor timer calls this; a window
    /// that is not open is left closed.
    func update(_ content: Content) {
        lastContent = content
        guard window?.isVisible == true else { return }
        render(content)
    }

    private func render(_ content: Content) {
        stack.arrangedSubviews.forEach { $0.removeFromSuperview() }
        switch content {
        case .checking:
            headline.stringValue = "Checking…"
            subhead.stringValue = "Running wt doctor"
            subhead.textColor = .secondaryLabelColor
            currentReportText = ""
        case .failed(let reason):
            headline.stringValue = "Doctor could not run"
            subhead.stringValue = reason
            subhead.textColor = .systemOrange
            currentReportText = reason
            stack.addArrangedSubview(
                paragraph("Nothing was checked, so nothing here is a verdict on your worktrees.")
            )
        case .report(let result, let staleReason):
            let report = DoctorCore.report(result)
            headline.stringValue = report.headline
            subhead.stringValue = subheadText(report, staleReason: staleReason)
            subhead.textColor = staleReason == nil ? .secondaryLabelColor : .systemOrange
            currentReportText = DoctorCore.formatReport(result)
            if report.sections.isEmpty {
                stack.addArrangedSubview(
                    paragraph("Every worktree doctor could reach matches what the registry says it should be.")
                )
            }
            for section in report.sections {
                stack.addArrangedSubview(sectionView(section))
            }
            if !report.notes.isEmpty {
                stack.addArrangedSubview(notesView(report.notes))
            }
        }
        copyReportButton?.isEnabled = !currentReportText.isEmpty
    }

    /// The line under the headline: how much of the machine the report
    /// covers, so a short report is not mistaken for a shallow one.
    private func subheadText(_ report: DoctorReport, staleReason: String?) -> String {
        if let staleReason {
            return "This report is out of date — the last check failed: \(staleReason)"
        }
        var parts: [String] = []
        if report.sections.isEmpty {
            parts.append("No findings across every repository the coordinator knows")
        } else {
            let apps = report.sections.count
            parts.append(apps == 1 ? "in 1 repository" : "across \(apps) repositories")
        }
        if !report.notes.isEmpty {
            parts.append(report.notes.count == 1 ? "1 thing was not checked" : "\(report.notes.count) things were not checked")
        }
        return parts.joined(separator: " · ")
    }

    // MARK: one section

    private func sectionView(_ section: DoctorSection) -> NSView {
        let isCollapsed = collapsed.contains(section.title)
        let worst = section.rows.map(\.level).max() ?? .info

        // A .disclosure-bezel button draws the triangle and nothing else,
        // so the app's name is its own label beside it.
        let disclosure = NSButton(title: "", target: self, action: #selector(toggleSection(_:)))
        disclosure.setButtonType(.onOff)
        disclosure.bezelStyle = .disclosure
        disclosure.state = isCollapsed ? .off : .on
        disclosure.identifier = NSUserInterfaceItemIdentifier(section.title)
        disclosure.setContentHuggingPriority(.required, for: .horizontal)

        let name = NSTextField(labelWithString: section.title)
        name.font = .systemFont(ofSize: 13, weight: .semibold)
        name.setContentHuggingPriority(.required, for: .horizontal)

        let count = NSTextField(labelWithString: countText(section.rows))
        count.font = .systemFont(ofSize: 11)
        count.textColor = colour(for: worst)

        let header = NSStackView(views: [disclosure, name, count, NSView()])
        header.orientation = .horizontal
        header.alignment = .centerY
        header.spacing = 8
        header.edgeInsets = NSEdgeInsets(top: 14, left: 0, bottom: 6, right: 0)

        let container = NSStackView(views: [header])
        container.orientation = .vertical
        container.alignment = .width
        container.spacing = 0
        if !isCollapsed {
            for row in section.rows {
                container.addArrangedSubview(rowView(row))
            }
        }
        return container
    }

    private func countText(_ rows: [DoctorRow]) -> String {
        let errors = rows.filter { $0.level == .error }.count
        let warnings = rows.filter { $0.level == .warning }.count
        let infos = rows.filter { $0.level == .info }.count
        var parts: [String] = []
        if errors > 0 { parts.append("\(errors) error\(errors == 1 ? "" : "s")") }
        if warnings > 0 { parts.append("\(warnings) warning\(warnings == 1 ? "" : "s")") }
        if infos > 0 { parts.append("\(infos) observation\(infos == 1 ? "" : "s")") }
        return parts.joined(separator: ", ")
    }

    // MARK: one finding

    private func rowView(_ row: DoctorRow) -> NSView {
        let icon = NSImageView()
        icon.image = NSImage(systemSymbolName: symbol(for: row.level), accessibilityDescription: row.level.rawValue)
        icon.contentTintColor = colour(for: row.level)
        icon.symbolConfiguration = NSImage.SymbolConfiguration(pointSize: 13, weight: .medium)
        icon.setContentHuggingPriority(.required, for: .horizontal)

        let title = wrappingLabel(row.title)
        title.font = .systemFont(ofSize: 13, weight: .medium)

        let body = NSStackView(views: [title])
        body.orientation = .vertical
        body.alignment = .width
        body.spacing = 4

        if let message = row.message {
            let label = wrappingLabel(message)
            label.font = .systemFont(ofSize: 12)
            label.textColor = .secondaryLabelColor
            body.addArrangedSubview(label)
        }
        for detail in row.details {
            let label = wrappingLabel("• \(detail)")
            label.font = .monospacedSystemFont(ofSize: 11, weight: .regular)
            label.textColor = .secondaryLabelColor
            body.addArrangedSubview(label)
        }
        if let remedy = row.remedy {
            body.addArrangedSubview(remedyView(remedy, path: row.path))
        } else if let path = row.path {
            body.addArrangedSubview(actionRow(buttons: [revealButton(path)]))
        }

        let container = NSStackView(views: [icon, body])
        container.orientation = .horizontal
        container.alignment = .top
        container.distribution = .fill
        container.spacing = 8
        container.edgeInsets = NSEdgeInsets(top: 6, left: 4, bottom: 10, right: 0)
        body.setContentHuggingPriority(.defaultLow, for: .horizontal)
        return container
    }

    /// The fix: the command in a monospaced, selectable field with the two
    /// buttons that act on it. Copying the command is what turns a remedy
    /// into something a person can run without retyping it.
    private func remedyView(_ remedy: String, path: String?) -> NSView {
        let label = wrappingLabel(remedy)
        label.font = .monospacedSystemFont(ofSize: 11, weight: .regular)
        label.isSelectable = true

        let box = NSView()
        box.wantsLayer = true
        box.layer?.backgroundColor = NSColor.quaternaryLabelColor.withAlphaComponent(0.12).cgColor
        box.layer?.cornerRadius = 5
        label.translatesAutoresizingMaskIntoConstraints = false
        box.addSubview(label)
        NSLayoutConstraint.activate([
            label.topAnchor.constraint(equalTo: box.topAnchor, constant: 6),
            label.bottomAnchor.constraint(equalTo: box.bottomAnchor, constant: -6),
            label.leadingAnchor.constraint(equalTo: box.leadingAnchor, constant: 8),
            label.trailingAnchor.constraint(equalTo: box.trailingAnchor, constant: -8),
        ])

        var buttons: [NSButton] = [linkButton("Copy fix", action: #selector(copyRemedyClicked(_:)), value: remedy)]
        if let path { buttons.append(revealButton(path)) }

        let stack = NSStackView(views: [box, actionRow(buttons: buttons)])
        stack.orientation = .vertical
        stack.alignment = .width
        stack.spacing = 4
        return stack
    }

    private func revealButton(_ path: String) -> NSButton {
        linkButton("Show in Finder", action: #selector(revealClicked(_:)), value: path)
    }

    private func linkButton(_ title: String, action: Selector, value: String) -> NSButton {
        let button = NSButton(title: title, target: self, action: action)
        button.bezelStyle = .inline
        button.controlSize = .small
        button.font = .systemFont(ofSize: 11)
        // The pasteboard string and the path to reveal ride on the button
        // rather than in a lookup table, so a re-render cannot leave a
        // button wired to a finding that is no longer on screen.
        button.identifier = NSUserInterfaceItemIdentifier(value)
        return button
    }

    private func actionRow(buttons: [NSButton]) -> NSView {
        let row = NSStackView(views: buttons + [NSView()])
        row.orientation = .horizontal
        row.spacing = 6
        return row
    }

    // MARK: the notes

    private func notesView(_ notes: [String]) -> NSView {
        let disclosure = NSButton(title: "", target: self, action: #selector(toggleNotes(_:)))
        disclosure.setButtonType(.onOff)
        disclosure.bezelStyle = .disclosure
        disclosure.state = notesCollapsed ? .off : .on
        disclosure.setContentHuggingPriority(.required, for: .horizontal)

        let name = NSTextField(labelWithString: "What doctor could not check (\(notes.count))")
        name.font = .systemFont(ofSize: 12)
        name.textColor = .secondaryLabelColor

        let heading = NSStackView(views: [disclosure, name, NSView()])
        heading.orientation = .horizontal
        heading.alignment = .centerY
        heading.spacing = 4

        let container = NSStackView(views: [separator(), heading])
        container.orientation = .vertical
        container.alignment = .width
        container.spacing = 10
        container.edgeInsets = NSEdgeInsets(top: 18, left: 0, bottom: 0, right: 0)
        if !notesCollapsed {
            for note in notes {
                let label = wrappingLabel("• \(note)")
                label.font = .systemFont(ofSize: 11)
                label.textColor = .secondaryLabelColor
                container.addArrangedSubview(label)
            }
        }
        return container
    }

    // MARK: actions

    @objc private func toggleSection(_ sender: NSButton) {
        guard let title = sender.identifier?.rawValue else { return }
        if collapsed.contains(title) {
            collapsed.remove(title)
        } else {
            collapsed.insert(title)
        }
        render(lastContent)
    }

    @objc private func toggleNotes(_ sender: NSButton) {
        notesCollapsed.toggle()
        render(lastContent)
    }

    @objc private func copyRemedyClicked(_ sender: NSButton) {
        guard let remedy = sender.identifier?.rawValue else { return }
        writeToPasteboard(remedy)
        flash(sender, title: "Copied")
    }

    @objc private func revealClicked(_ sender: NSButton) {
        guard let path = sender.identifier?.rawValue else { return }
        NSWorkspace.shared.activateFileViewerSelecting([URL(fileURLWithPath: path)])
    }

    @objc private func copyReportClicked(_ sender: NSButton) {
        writeToPasteboard(currentReportText)
        flash(sender, title: "Copied")
    }

    @objc private func recheckClicked() {
        render(.checking)
        recheck()
    }

    @objc private func doneClicked() {
        window?.close()
    }

    private func writeToPasteboard(_ string: String) {
        let pasteboard = NSPasteboard.general
        pasteboard.clearContents()
        pasteboard.setString(string, forType: .string)
    }

    /// Confirms a copy in the button itself. A copy that changes nothing on
    /// screen leaves the person wondering whether it worked.
    private func flash(_ button: NSButton, title: String) {
        let original = button.title
        button.title = title
        DispatchQueue.main.asyncAfter(deadline: .now() + 1.2) { [weak button] in
            button?.title = original
        }
    }

    // MARK: small AppKit helpers

    /// The symbol and the colour a level is shown in. Colour alone would
    /// leave the three levels indistinguishable to a reader who cannot tell
    /// them apart, so each level carries its own shape too.
    private func symbol(for level: DoctorLevel) -> String {
        switch level {
        case .error: return "xmark.octagon.fill"
        case .warning: return "exclamationmark.triangle.fill"
        case .info: return "info.circle"
        }
    }

    private func colour(for level: DoctorLevel) -> NSColor {
        switch level {
        case .error: return .systemRed
        case .warning: return .systemOrange
        case .info: return .secondaryLabelColor
        }
    }

    private func wrappingLabel(_ text: String) -> NSTextField {
        let label = NSTextField(wrappingLabelWithString: text)
        label.isSelectable = true
        // A wrapping label in a width-aligned stack otherwise picks its own
        // alignment from the text, and a report reads down the left edge.
        label.alignment = .left
        label.lineBreakMode = .byWordWrapping
        label.setContentCompressionResistancePriority(.defaultLow, for: .horizontal)
        // A label that hugs its text is left at its natural width by the
        // stack and pushed to the trailing edge; one that does not is
        // stretched to the column, which is what a report reads like.
        label.setContentHuggingPriority(.init(1), for: .horizontal)
        return label
    }

    private func paragraph(_ text: String) -> NSView {
        let label = wrappingLabel(text)
        label.textColor = .secondaryLabelColor
        let container = NSStackView(views: [label])
        container.orientation = .vertical
        container.alignment = .width
        container.edgeInsets = NSEdgeInsets(top: 12, left: 0, bottom: 0, right: 0)
        return container
    }

    private func separator() -> NSBox {
        let box = NSBox()
        box.boxType = .separator
        return box
    }

}

/// A top-left origin for the scroll view's document, so the report starts
/// at the top of the window rather than the bottom.
private final class FlippedView: NSView {
    override var isFlipped: Bool { true }
}
