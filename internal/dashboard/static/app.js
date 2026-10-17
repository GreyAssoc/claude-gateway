"use strict";

const POLL_INTERVAL = 30000;

function esc(s) {
    return String(s ?? "").replace(/[&<>"']/g, (ch) => ({
        "&": "&amp;",
        "<": "&lt;",
        ">": "&gt;",
        "\"": "&quot;",
        "'": "&#39;"
    }[ch]));
}

// Fetch helper with optional auth
async function api(path) {
    const headers = {};
    const token = localStorage.getItem("dashboard_token");
    if (token) {
        headers["Authorization"] = "Bearer " + token;
    }
    const resp = await fetch(path, { headers });
    if (!resp.ok) {
        throw new Error("API error: " + resp.status);
    }
    return resp.json();
}

// Status
async function loadStatus() {
    try {
        const data = await api("/api/status");
        const el = document.getElementById("status-content");
        el.innerHTML = `
            <table>
                <tr><th>Status</th><td><span class="badge badge-green">${data.status}</span></td></tr>
                <tr><th>Uptime</th><td>${data.uptime}</td></tr>
                <tr><th>Version</th><td>${data.version}</td></tr>
            </table>`;
        document.getElementById("status-indicator").className = "status-dot ok";
    } catch (e) {
        document.getElementById("status-content").textContent = "Error: " + e.message;
        document.getElementById("status-indicator").className = "status-dot error";
    }
}

// Rate Limits
const usageHistory = [];
async function loadRateLimits() {
    try {
        const data = await api("/api/rate-limits");
        const c = data.current;
        const headroomPct = Math.round(c.headroom * 100);
        const meterClass = headroomPct > 50 ? "low" : headroomPct > 20 ? "medium" : "high";

        const conv = Array.isArray(c.conversations) ? c.conversations : [];
        let convRows = "";
        for (const row of conv) {
            const pct = row.context_window ? Math.round(100 * row.context_tokens / row.context_window) : 0;
            convRows += `<tr><td>${esc(row.framework)}</td><td>${esc(row.session_id)}</td><td>${esc(row.model)}</td><td>${row.context_tokens} / ${row.context_window} (${pct}%)</td><td>${row.output_tokens}</td></tr>`;
        }
        if (!convRows) {
            convRows = `<tr><td colspan="5">No conversations yet</td></tr>`;
        }
        document.getElementById("rate-limits-content").innerHTML = `
            <table>
                <tr><th>RPM</th><td>${esc(c.rpm)}</td></tr>
                <tr><th>Request headroom</th><td>${headroomPct}%
                    <div class="meter"><div class="meter-fill ${meterClass}" style="width:${headroomPct}%"></div></div>
                </td></tr>
                <tr><th>Queue</th><td>${esc(c.queue_depth)}</td></tr>
                <tr><th>Window Resets</th><td>${esc(c.window_resets)}</td></tr>
            </table>
            <p>Each conversation is counted against its own context window. Agents are not added together.</p>
            <table>
                <tr><th>Framework</th><th>Session</th><th>Model</th><th>Context</th><th>Output this turn</th></tr>
                ${convRows}
            </table>`;

        // Track history for chart
        usageHistory.push({ time: Date.now(), headroom: c.headroom });
        if (usageHistory.length > 60) usageHistory.shift();
        drawChart();
    } catch (e) {
        document.getElementById("rate-limits-content").textContent = "Error: " + e.message;
    }
}

function drawChart() {
    const canvas = document.getElementById("usage-chart");
    if (!canvas || usageHistory.length < 2) return;

    const ctx = canvas.getContext("2d");
    const w = canvas.width;
    const h = canvas.height;
    const padding = 30;

    ctx.clearRect(0, 0, w, h);

    // Background
    ctx.fillStyle = "#161b22";
    ctx.fillRect(0, 0, w, h);

    // Grid
    ctx.strokeStyle = "#30363d";
    ctx.lineWidth = 0.5;
    for (let i = 0; i <= 4; i++) {
        const y = padding + (h - 2 * padding) * (i / 4);
        ctx.beginPath();
        ctx.moveTo(padding, y);
        ctx.lineTo(w - padding, y);
        ctx.stroke();
    }

    // Draw headroom line
    ctx.strokeStyle = "#58a6ff";
    ctx.lineWidth = 2;
    ctx.beginPath();
    const points = usageHistory;
    for (let i = 0; i < points.length; i++) {
        const x = padding + (w - 2 * padding) * (i / (points.length - 1));
        const y = padding + (h - 2 * padding) * (1 - points[i].headroom);
        if (i === 0) ctx.moveTo(x, y);
        else ctx.lineTo(x, y);
    }
    ctx.stroke();

    // Labels
    ctx.fillStyle = "#8b949e";
    ctx.font = "11px sans-serif";
    ctx.fillText("100%", 2, padding + 4);
    ctx.fillText("0%", 2, h - padding + 4);
    ctx.fillText("Headroom", w / 2 - 30, h - 5);
}

// Skills
async function loadSkills() {
    try {
        const data = await api("/api/skills");
        if (!data || data.length === 0) {
            document.getElementById("skills-content").textContent = "No skills loaded.";
            return;
        }
        let html = "<table><tr><th>Name</th><th>Status</th><th>Precedence</th><th>Size</th><th>Action</th></tr>";
        for (const s of data) {
            const badge = s.enabled ? '<span class="badge badge-green">ON</span>' : '<span class="badge badge-red">OFF</span>';
            html += `<tr>
                <td>${s.name}</td>
                <td>${badge}</td>
                <td>${s.precedence}</td>
                <td>${s.content_length}</td>
                <td><button class="toggle-btn" onclick="toggleSkill('${s.name}')">Toggle</button></td>
            </tr>`;
        }
        html += "</table>";
        document.getElementById("skills-content").innerHTML = html;
    } catch (e) {
        document.getElementById("skills-content").textContent = "Error: " + e.message;
    }
}

async function toggleSkill(name) {
    try {
        const headers = { "Content-Type": "application/json" };
        const token = localStorage.getItem("dashboard_token");
        if (token) headers["Authorization"] = "Bearer " + token;
        await fetch("/api/skills/" + name + "/toggle", { method: "POST", headers });
        loadSkills();
    } catch (e) {
        console.error("Toggle error:", e);
    }
}

// Memory
async function loadMemory() {
    try {
        const data = await api("/api/memory");
        if (!data || data.length === 0) {
            document.getElementById("memory-content").textContent = "No curated memories.";
            return;
        }
        let html = "<table><tr><th>ID</th><th>Category</th><th>Content</th><th>Importance</th></tr>";
        for (const m of data) {
            const content = m.Content.length > 80 ? m.Content.substring(0, 80) + "..." : m.Content;
            html += `<tr>
                <td>${m.ID}</td>
                <td><span class="badge badge-blue">${m.Category}</span></td>
                <td class="truncate">${escapeHtml(content)}</td>
                <td>${m.Importance}</td>
            </tr>`;
        }
        html += "</table>";
        document.getElementById("memory-content").innerHTML = html;
    } catch (e) {
        document.getElementById("memory-content").textContent = "Error: " + e.message;
    }
}

// Sessions
async function loadSessions() {
    try {
        const data = await api("/api/sessions");
        if (!data || data.length === 0) {
            document.getElementById("sessions-content").textContent = "No recent sessions.";
            return;
        }
        let html = "<table><tr><th>Session</th><th>Source</th><th>In</th><th>Out</th><th>Time</th></tr>";
        for (const s of data) {
            html += `<tr>
                <td>${s.session_id}</td>
                <td>${s.source}</td>
                <td>${formatTokens(s.input_tokens)}</td>
                <td>${formatTokens(s.output_tokens)}</td>
                <td>${formatTime(s.created_at)}</td>
            </tr>`;
        }
        html += "</table>";
        document.getElementById("sessions-content").innerHTML = html;
    } catch (e) {
        document.getElementById("sessions-content").textContent = "Error: " + e.message;
    }
}

// Agents
async function loadAgents() {
    try {
        const data = await api("/api/agents");
        if (!data || data.length === 0) {
            document.getElementById("agents-content").textContent = "No agents.";
            return;
        }

        // Group by team
        const teams = {};
        for (const a of data) {
            if (!teams[a.team]) teams[a.team] = [];
            teams[a.team].push(a);
        }

        let html = "<table><tr><th>Team</th><th>Agent</th><th>Tier</th><th>Veto</th></tr>";
        for (const [team, agents] of Object.entries(teams)) {
            for (const a of agents) {
                const veto = a.has_veto ? '<span class="badge badge-red">VETO</span>' : '';
                html += `<tr>
                    <td>${team}</td>
                    <td>${a.name}</td>
                    <td>T${a.tier}</td>
                    <td>${veto}</td>
                </tr>`;
            }
        }
        html += "</table>";
        document.getElementById("agents-content").innerHTML = html;
    } catch (e) {
        document.getElementById("agents-content").textContent = "Error: " + e.message;
    }
}

// Orchestrator Runs
async function loadRuns() {
    try {
        const data = await api("/api/orchestrator/runs");
        if (!data || data.length === 0) {
            document.getElementById("runs-content").textContent = "No runs yet.";
            return;
        }
        let html = "<table><tr><th>ID</th><th>Team</th><th>Outcome</th><th>Task</th><th>Duration</th><th>Time</th></tr>";
        for (const r of data) {
            const badgeClass = r.outcome === "approved" ? "badge-green" : r.outcome === "blocked" ? "badge-red" : "badge-yellow";
            const task = r.task.length > 50 ? r.task.substring(0, 50) + "..." : r.task;
            html += `<tr>
                <td>${r.id}</td>
                <td>${r.team}</td>
                <td><span class="badge ${badgeClass}">${r.outcome}</span></td>
                <td class="truncate">${escapeHtml(task)}</td>
                <td>${(r.duration_ms / 1000).toFixed(1)}s</td>
                <td>${formatTime(r.created_at)}</td>
            </tr>`;
        }
        html += "</table>";
        document.getElementById("runs-content").innerHTML = html;
    } catch (e) {
        document.getElementById("runs-content").textContent = "Error: " + e.message;
    }
}

// Helpers
function formatTokens(n) {
    if (n >= 1000000) return (n / 1000000).toFixed(1) + "M";
    if (n >= 1000) return (n / 1000).toFixed(1) + "k";
    return String(n);
}

function formatTime(isoStr) {
    if (!isoStr) return "";
    try {
        const d = new Date(isoStr);
        return d.toLocaleTimeString();
    } catch {
        return isoStr;
    }
}

function escapeHtml(str) {
    const div = document.createElement("div");
    div.textContent = str;
    return div.innerHTML;
}

// Poll loop
async function refresh() {
    await Promise.allSettled([
        loadStatus(),
        loadRateLimits(),
        loadSkills(),
        loadMemory(),
        loadSessions(),
        loadAgents(),
        loadRuns(),
    ]);
    document.getElementById("last-updated").textContent = "Last updated: " + new Date().toLocaleTimeString();
}

refresh();
setInterval(refresh, POLL_INTERVAL);
