-- rex-beacon for Rex: Attention Inbox, Blocked-Pane Remote Reply (Mac & iPhone),
-- Pane Output Peek, and Wrapped Command Runner with OSC 7501 + Feishu alerts.
--
-- Installed by `rex-beacon rex install`; `rex-beacon rex uninstall` removes it.

local M = {}

local TERMINAL = "com.superlogical.terminal"
local SHELL = "com.superlogical.terminal.shell"

local STATE_RANK = {
  blocked = 1,
  error = 2,
  done = 3,
  working = 4,
  idle = 5,
}

local STATE_ICON = {
  blocked = "clock",
  error = "gear",
  done = "star",
  working = "terminal",
  idle = "code",
}

local STATE_BADGE = {
  blocked = "BLOCKED",
  error = "ERROR",
  done = "DONE",
  working = "WORKING",
  idle = "IDLE",
}

local function shell_quote(s)
  return "'" .. tostring(s):gsub("'", "'\\''") .. "'"
end

local function short_id(id)
  if type(id) ~= "string" then return "" end
  return id:match("([^%-]+)$") or id
end

local function collect_records(only_blocked)
  local list_res, err = rex.call("session.list")
  if err ~= nil or type(list_res) ~= "table" then
    return {}
  end

  local entries = {}
  for _, sess in ipairs(list_res.sessions or {}) do
    local view = rex.session.view{ session_id = sess.session_id }
    local win_map = {}
    local blk_map = {}
    if type(view) == "table" then
      for _, win in ipairs(view.windows or {}) do
        for _, layer in ipairs(win.layers or {}) do
          for _, blk in ipairs(layer.blocks or {}) do
            win_map[blk.block_id] = win.label or short_id(win.window_id)
            blk_map[blk.block_id] = blk.label or "shell"
          end
        end
      end
    end

    local blocks = rex.session.list_blocks{ session_id = sess.session_id }
    if type(blocks) == "table" then
      for _, b in ipairs(blocks.blocks or {}) do
        if b.creator_name == TERMINAL then
          local st = rex.block.call(TERMINAL, "program_status", { block_id = b.block_id })
          if type(st) == "table" and type(st.records) == "table" then
            for _, rec in ipairs(st.records) do
              if not only_blocked or rec.state == "blocked" then
                local app = rec.effective_app or rec.app or "terminal"
                local rank = STATE_RANK[rec.state] or 9
                table.insert(entries, {
                  rank = rank,
                  session_id = sess.session_id,
                  session_label = sess.label or short_id(sess.session_id),
                  window_label = win_map[b.block_id] or "",
                  block_id = b.block_id,
                  block_label = blk_map[b.block_id] or b.label or "shell",
                  state = rec.state or "idle",
                  kind = rec.kind or "",
                  app = app,
                  title = rec.title or "",
                  msg = rec.msg or "",
                  updated_at = rec.updated_at or "",
                })
              end
            end
          end
        end
      end
    end
  end

  table.sort(entries, function(a, b)
    if a.rank ~= b.rank then return a.rank < b.rank end
    return a.updated_at > b.updated_at
  end)
  return entries
end

local function build_pick_items(entries)
  local items = {}
  local lookup = {}
  for i, e in ipairs(entries) do
    local item_id = e.block_id .. "#" .. tostring(i)
    lookup[item_id] = e
    local title = string.format("[%s] %s", STATE_BADGE[e.state] or string.upper(e.state), e.app)
    if e.title ~= "" then
      title = title .. " · " .. e.title
    end
    local sub = e.session_label
    if e.window_label ~= "" then sub = sub .. " / " .. e.window_label end
    if e.msg ~= "" then
      sub = sub .. " — " .. e.msg
    end
    local detail = e.kind ~= "" and e.kind or e.block_label
    table.insert(items, {
      id = item_id,
      title = title,
      subtitle = sub,
      icon = STATE_ICON[e.state] or "terminal",
      detail = detail,
    })
  end
  return items, lookup
end

function M.setup(opts)
  opts = opts or {}
  local bin = opts.bin or { "rex-beacon" }
  if type(bin) == "string" then bin = { bin } end

  local function argv(...)
    local out = {}
    for _, p in ipairs(bin) do out[#out + 1] = p end
    for _, p in ipairs({ ... }) do out[#out + 1] = p end
    return out
  end

  rex.action{
    name = "beacon_inbox",
    title = "Beacon: Attention Inbox…",
    description = "List all blocked, errored, finished, and active panes across Rex sessions and jump directly to one.",
    category = "Beacon",
    keywords = "beacon inbox status blocked error done notification attention jump",
    run = function(ctx)
      local entries = collect_records(false)
      if #entries == 0 then
        rex.ui.pick{
          title = "Beacon: Attention Inbox (Empty)",
          placeholder = "No active OSC 7501 status records found",
          items = { { id = "ok", title = "All quiet — no programs reporting status", icon = "star" } },
        }
        return
      end

      local items, lookup = build_pick_items(entries)
      local choice = rex.ui.pick{
        title = "Beacon: Attention Inbox",
        placeholder = "Filter by app, session, state, or message",
        items = items,
      }
      if choice == nil then return end
      local target = lookup[choice]
      if target == nil then return end

      rex.session.focus_block{ session_id = target.session_id, block_id = target.block_id }
      if ctx.client_id ~= nil then
        rex.client.queue("session.select", { session_id = target.session_id })
      end
      return {
        session_id = target.session_id,
        block_id = target.block_id,
        state = target.state,
      }
    end,
  }

  rex.action{
    name = "beacon_reply",
    title = "Beacon: Reply to Blocked Pane…",
    description = "Send quick confirmation (y/n/Enter) or custom input to any blocked terminal pane without switching first (works from Mac & iPhone).",
    category = "Beacon",
    keywords = "beacon reply unblock permission input approve yes no remote iphone",
    args = { block_id = "string?", text = "string?" },
    run = function(ctx, args)
      local target_block = args.block_id
      local target_session = ctx.session_id

      if target_block == nil or target_block == "" then
        local blocked = collect_records(true)
        if #blocked == 0 then
          blocked = collect_records(false)
        end
        if #blocked == 0 then
          rex.ui.pick{
            title = "Beacon: No Panes Reporting Status",
            items = { { id = "none", title = "No blocked or status-reporting panes found", icon = "terminal" } },
          }
          return
        elseif #blocked == 1 then
          target_block = blocked[1].block_id
          target_session = blocked[1].session_id
        else
          local items, lookup = build_pick_items(blocked)
          local picked = rex.ui.pick{
            title = "Beacon: Choose Pane to Reply",
            placeholder = "Select a blocked pane",
            items = items,
          }
          if picked == nil then return end
          local e = lookup[picked]
          if e == nil then return end
          target_block = e.block_id
          target_session = e.session_id
        end
      end

      local payload = args.text
      if payload == nil then
        local action = rex.ui.pick{
          title = "Send Input to Pane (" .. short_id(target_block) .. ")",
          placeholder = "Choose quick reply or type custom input",
          allow_other = true,
          items = {
            { id = "yes", title = "Approve: y + Enter", subtitle = "Send y\\r", icon = "star" },
            { id = "one", title = "Select Option 1 + Enter", subtitle = "Send 1\\r", icon = "code" },
            { id = "enter", title = "Press Enter", subtitle = "Send \\r", icon = "terminal" },
            { id = "no", title = "Decline: n + Enter", subtitle = "Send n\\r", icon = "gear" },
            { id = "ctrlc", title = "Interrupt (Ctrl+C)", subtitle = "Send \\x03", icon = "gear" },
            { id = "custom", title = "Type Custom Text…", subtitle = "Open text prompt", icon = "document" },
          },
        }
        if action == nil then return end
        if type(action) == "table" then
          payload = action.text .. "\r"
        elseif action == "yes" then
          payload = "y\r"
        elseif action == "one" then
          payload = "1\r"
        elseif action == "enter" then
          payload = "\r"
        elseif action == "no" then
          payload = "n\r"
        elseif action == "ctrlc" then
          payload = "\x03"
        elseif action == "custom" then
          local txt = rex.ui.input{
            title = "Reply to Pane",
            placeholder = "Text to type (Enter will be appended)",
            confirm = "Send",
          }
          if txt == nil then return end
          payload = txt .. "\r"
        end
      end

      local _, err = rex.block.call(TERMINAL, "write", {
        block_id = target_block,
        data = payload,
      })
      if err ~= nil then error("beacon_reply: " .. tostring(err), 0) end
      return { session_id = target_session, block_id = target_block, sent = true }
    end,
  }

  rex.action{
    name = "beacon_peek",
    title = "Beacon: Peek Pane Output…",
    description = "Open a floating window showing the recent output of any status-reporting pane.",
    category = "Beacon",
    keywords = "beacon peek tail logs output float layer",
    run = function(ctx)
      local entries = collect_records(false)
      if #entries == 0 then
        return { message = "No status-reporting panes found" }
      end
      local items, lookup = build_pick_items(entries)
      local choice = rex.ui.pick{
        title = "Beacon: Peek Pane Tail",
        items = items,
      }
      if choice == nil then return end
      local target = lookup[choice]
      if target == nil then return end

      local cmd = argv("peek", "--lines", "40", "--wait", target.block_id)
      local _, err = rex.session.new_layer{
        session_id = ctx.session_id,
        bounds = { 0.12, 0.12, 0.76, 0.72 },
        focus = true,
        layout = {
          block = {
            flavor = SHELL,
            label = "Peek: " .. target.app,
            options = { command = cmd },
          },
        },
      }
      if err ~= nil then error("beacon_peek: " .. tostring(err), 0) end
    end,
  }

  rex.action{
    name = "beacon_run",
    title = "Beacon: Run Command & Notify…",
    description = "Run a shell command in a new split wrapped with OSC 7501 status reporting and Feishu/macOS alerts on finish.",
    category = "Beacon",
    keywords = "beacon run wrap command notify build test",
    args = { command = "string?" },
    run = function(ctx, args)
      local cmdline = args.command
      if cmdline == nil or cmdline == "" then
        cmdline = rex.ui.input{
          title = "Run & Notify",
          placeholder = "e.g. go test ./... or npm run build",
          confirm = "Run in Split",
        }
        if cmdline == nil or cmdline == "" then return end
      end

      local cmd = argv("wrap", "--title", cmdline, "--", "sh", "-c", cmdline)
      local layout = {
        block = {
          flavor = SHELL,
          label = "Beacon Run",
          options = { command = cmd, exit = { on_completion = false } },
        },
      }
      if ctx.block_id == nil then
        local res, err = rex.session.new_window{ label = "Beacon Run", layout = layout, focus = true }
        if err ~= nil then error(err, 0) end
        return res
      end
      local res, err = rex.session.new_split{
        anchor_id = ctx.block_id,
        direction = "vertical",
        side = "after",
        ratio = 0.65,
        focus = false,
        layout = layout,
      }
      if err ~= nil then error(err, 0) end
      return res
    end,
  }

  rex.action{
    name = "beacon_notify_test",
    title = "Beacon: Send Test Alert",
    description = "Send a test notification through all configured rex-beacon channels (Feishu/Lark + macOS).",
    category = "Beacon",
    keywords = "beacon test feishu lark notification alert",
    run = function(ctx)
      local args = { "notify", "--state", "blocked", "--kind", "permission", "--app", "rex-beacon", "--msg", "Test alert triggered from Rex Command Palette" }
      if ctx.session_id ~= nil then
        table.insert(args, "--session")
        table.insert(args, ctx.session_id)
      end
      if ctx.block_id ~= nil then
        table.insert(args, "--block")
        table.insert(args, ctx.block_id)
      end
      local unpack_fn = table.unpack or unpack
      local res, err = rex.exec(argv(unpack_fn(args)), { timeout = 15 })
      if res == nil then error("beacon_notify_test: " .. tostring(err), 0) end
      if not res.ok then error("beacon_notify_test: " .. tostring(res.stderr), 0) end
      return { ok = true, output = res.stdout }
    end,
  }

  rex.action{
    name = "beacon_daemon",
    title = "Beacon: Daemon Control…",
    description = "Check status, start, or stop the rex-beacon event daemon.",
    category = "Beacon",
    keywords = "beacon daemon watch start stop status",
    run = function()
      local st = rex.exec(argv("daemon", "status"), { timeout = 5 })
      local status_line = (st and st.stdout or "unknown"):gsub("%s+$", "")
      local action = rex.ui.pick{
        title = "Beacon Daemon (" .. status_line .. ")",
        items = {
          { id = "status", title = "Refresh Status", subtitle = status_line, icon = "server" },
          { id = "start", title = "Start Background Daemon", subtitle = "Listen to all Rex events", icon = "plus" },
          { id = "stop", title = "Stop Background Daemon", subtitle = "Stop listening", icon = "gear" },
        },
      }
      if action == nil then return end
      local res, err = rex.exec(argv("daemon", action), { timeout = 10 })
      if res == nil then error(err, 0) end
      if not res.ok then error(res.stderr, 0) end
      return { status = res.stdout }
    end,
  }
end

return M
