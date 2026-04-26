package com.relayone.r1

import com.intellij.openapi.actionSystem.AnAction
import com.intellij.openapi.actionSystem.AnActionEvent
import com.intellij.openapi.actionSystem.CommonDataKeys
import com.intellij.openapi.application.ApplicationManager
import com.intellij.openapi.application.ModalityState
import com.intellij.openapi.editor.Editor
import com.intellij.openapi.fileEditor.FileDocumentManager
import com.intellij.openapi.progress.ProgressIndicator
import com.intellij.openapi.progress.Task
import com.intellij.openapi.project.Project
import com.intellij.openapi.ui.Messages
import com.intellij.openapi.wm.ToolWindowManager
import com.intellij.notification.NotificationGroupManager
import com.intellij.notification.NotificationType

/** Opens (and focuses) the R1 chat tool window. */
class OpenChatAction : AnAction() {
    override fun actionPerformed(e: AnActionEvent) {
        val project = e.project ?: return
        val win = ToolWindowManager.getInstance(project).getToolWindow("R1 Chat") ?: return
        win.show(null)
        win.activate(null, true)
    }
}

/**
 * Prompts for an objective and submits it to the daemon as a task.
 * Result is shown in a balloon notification; full body in the
 * Messages dialog when oversized.
 */
class RunTaskAction : AnAction() {
    override fun actionPerformed(e: AnActionEvent) {
        val project = e.project ?: return
        val objective = Messages.showInputDialog(
            project,
            "Describe the task for r1-agent:",
            "R1: Run Task",
            Messages.getQuestionIcon(),
            "",
            null,
        )?.trim().orEmpty()
        if (objective.isEmpty()) return

        runTaskInBackground(project, "R1: running task") {
            val settings = R1Settings.getInstance()
            settings.newClient().submitTask(
                TaskRequestBody(
                    taskType = settings.effectiveTaskType(),
                    description = objective,
                    extra = mapOf("source" to "jetbrains", "command" to "r1.run.task"),
                )
            )
        }
    }
}

/**
 * Sends the active editor's selection (and filename) to the daemon
 * for explanation. Surfaces "no selection" as a balloon error rather
 * than a noisy modal dialog.
 */
class ExplainSelectionAction : AnAction() {
    override fun actionPerformed(e: AnActionEvent) {
        val project = e.project ?: return
        val editor: Editor = e.getData(CommonDataKeys.EDITOR) ?: run {
            notifyError(project, "Open a file and select some text first.")
            return
        }
        val selectionText = editor.selectionModel.selectedText.orEmpty()
        if (selectionText.isBlank()) {
            notifyError(project, "Selection is empty.")
            return
        }
        val filename = FileDocumentManager.getInstance().getFile(editor.document)?.path
            ?: "<unsaved>"

        runTaskInBackground(project, "R1: explaining selection") {
            val settings = R1Settings.getInstance()
            settings.newClient().submitTask(
                TaskRequestBody(
                    taskType = settings.effectiveTaskType(),
                    description = "Explain the following code from $filename",
                    query = selectionText,
                    extra = mapOf(
                        "source" to "jetbrains",
                        "command" to "r1.explain.selection",
                        "filename" to filename,
                    ),
                )
            )
        }
    }
}

private fun runTaskInBackground(project: Project, title: String, work: () -> TaskState) {
    object : Task.Backgroundable(project, title, false) {
        override fun run(indicator: ProgressIndicator) {
            indicator.isIndeterminate = true
            val resultMsg: String = try {
                val state = work()
                val body = state.summary ?: state.error ?: "(no body)"
                "status=${state.status}\n\n$body"
            } catch (e: DaemonHttpException) {
                "${e.message}"
            } catch (e: Exception) {
                "${e.javaClass.simpleName}: ${e.message}"
            }
            ApplicationManager.getApplication().invokeLater({
                showResult(project, title, resultMsg)
            }, ModalityState.any())
        }
    }.queue()
}

private fun showResult(project: Project, title: String, body: String) {
    val truncated = if (body.length > 600) body.take(600) + "..." else body
    NotificationGroupManager.getInstance()
        .getNotificationGroup("R1 Agent")
        .createNotification(title, truncated, NotificationType.INFORMATION)
        .notify(project)
    if (body.length > 600) {
        Messages.showInfoMessage(project, body, title)
    }
}

private fun notifyError(project: Project, msg: String) {
    NotificationGroupManager.getInstance()
        .getNotificationGroup("R1 Agent")
        .createNotification("R1 Agent", msg, NotificationType.WARNING)
        .notify(project)
}
