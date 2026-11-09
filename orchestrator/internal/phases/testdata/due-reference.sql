SELECT r.id, r.organization_id, COALESCE(r.project_id, ''), COALESCE(r.task_id, ''), COALESCE(r.session_id, ''),
	COALESCE(r.phase::text, ''), r.status::text, r.control::text,
	COALESCE(r.role::text, ''),
	COALESCE(r.category, ''),
	COALESCE(r.lux_run_id, ''), COALESCE(r.lux_state, ''), COALESCE(r.lux_stop_reason, ''),
	COALESCE(r.push_request_id, ''), COALESCE(r.push_branch, ''), cardinality(r.lux_pushes) > 0, r.base_refs, r.base_shas, r.turn_done_at IS NOT NULL,
	EXISTS (SELECT 1 FROM directives d WHERE d.run_id = r.id AND d.sent_at IS NULL AND d.failed_at IS NULL AND NOT ((d.held_for IS NOT NULL AND EXISTS (SELECT 1 FROM questions hq WHERE hq.id = d.held_for AND hq.status = 'open'))
	OR (d.deliver_after IS NOT NULL AND d.deliver_after > now()))
		AND NOT COALESCE(d.created_at > hold.cutoff AND NOT d.interrupt AND d.deliver_after IS NULL, false)),
	CASE WHEN r.turn_done_at IS NOT NULL AND r.status = 'running' AND r.lux_state = 'running' AND r.push_request_id IS NULL
	THEN EXISTS (SELECT 1 FROM directives d WHERE d.run_id = r.id AND d.sent_at IS NOT NULL AND d.failed_at IS NULL
	        AND d.interrupt_only IS NOT TRUE
	        AND d.sent_at > now() - make_interval(secs => 120)
	        AND (d.delivered_at IS NULL OR (SELECT max(e.cursor) FROM events e WHERE e.run_id = r.id
	              AND e.event_type = 'run.directive.delivered' AND e.payload->>'directiveId' = d.id)
	            > (SELECT max(e.cursor) FROM events e WHERE e.run_id = r.id AND e.event_type = 'agent.session.stopped')))
	ELSE false END,
	(EXISTS (SELECT 1 FROM repository_requests q JOIN repositories repo ON repo.id = q.repository_id
	        WHERE q.run_id = r.id AND q.status = 'approved' AND NOT (lux_name(repo.name) = ANY (r.lux_repositories)))
	OR (r.session_id IS NOT NULL AND r.lux_run_id IS NOT NULL AND EXISTS (SELECT 1 FROM session_repositories sr
	JOIN repositories repo ON repo.id = sr.repository_id JOIN projects p ON p.id = repo.project_id
	JOIN session_projects spj ON spj.session_id = sr.session_id AND spj.project_id = repo.project_id
	WHERE sr.session_id = r.session_id AND NOT EXISTS (SELECT 1 FROM sessions fs JOIN chat_hidden_projects hp ON hp.provider = fs.origin->>'provider'
		WHERE fs.id = sr.session_id AND hp.project_id = repo.project_id)
	  AND NOT (lux_name(p.key_prefix || '-' || repo.name) = ANY (r.lux_repositories))))),
	COALESCE(r.dude_pause, ''), ask.open,
	CASE WHEN r.waiting_since IS NULL OR NOT ask.open THEN 'none'
		WHEN hold.held THEN CASE WHEN ((EXISTS (SELECT 1 FROM directives d WHERE d.run_id = r.id AND d.delivered_at IS NULL AND d.sent_at IS NULL AND d.failed_at IS NULL
		AND NOT ((d.held_for IS NOT NULL AND EXISTS (SELECT 1 FROM questions hq WHERE hq.id = d.held_for AND hq.status = 'open'))
	OR (d.deliver_after IS NOT NULL AND d.deliver_after > now()))
		AND d.created_at <= COALESCE(hold.cutoff, 'infinity')
		AND (d.claimed_at IS NULL OR d.claimed_at > now() - make_interval(secs => 120)))
	OR EXISTS (SELECT 1 FROM directives d WHERE d.run_id = r.id AND d.failed_at IS NULL AND d.interrupt_only IS NOT TRUE
		AND (d.delivered_at IS NULL OR d.delivered_at > r.waiting_since)
		AND d.created_at <= COALESCE(hold.cutoff, 'infinity')
		AND d.sent_at > now() - make_interval(secs => 120))) AND COALESCE(GREATEST(LEAST((SELECT min(q.asked_at) FROM questions q WHERE q.run_id = r.id AND q.status = 'open'),
	(SELECT min(q.created_at) FROM repository_requests q WHERE q.run_id = r.id AND q.status = 'pending' AND q.blocking),
	(SELECT min((w.state->'escalation'->>'raisedAt')::timestamptz) FROM workflow_runs w WHERE w.task_id = r.task_id AND w.workflow_type = 'task.delivery'
	AND w.status = 'waiting' AND w.state->'escalation'->>'reason' = 'checks_unverified'
	AND w.state->'escalation'->'detail'->>'runId' = r.id AND NOT (w.state->'escalation' ? 'decided')),
	(SELECT min(pw.since) FROM peer_waits pw WHERE pw.run_id = r.id AND pw.closed_at IS NULL),
	(SELECT min(a.occurred_at) FROM events a WHERE a.run_id = r.id AND a.event_type = 'run.checks.asked'
	AND (a.payload->>'deadline')::timestamptz > now()
	AND (a.payload->>'turn')::bigint = (SELECT COALESCE(max(t.cursor), 0) FROM events t WHERE t.run_id = r.id AND t.event_type = 'agent.session.stopped')
	AND NOT EXISTS (SELECT 1 FROM events v WHERE v.run_id = r.id AND v.event_type IN ('run.checks.conducted', 'run.checks.judged')
		AND v.payload->>'turn' = a.payload->>'turn'))),
	(SELECT max(rr.requested_at) FROM run_resumes rr WHERE rr.run_id = r.id),
	(SELECT max(u.occurred_at) FROM events u WHERE u.run_id = r.id AND u.event_type = 'run.unparked')) > now() - make_interval(secs => 2 * lim.park_secs), false)) THEN 'none' ELSE 'park' END
		WHEN r.waiting_since < now() - make_interval(secs => lim.park_secs) THEN CASE WHEN ((EXISTS (SELECT 1 FROM directives d WHERE d.run_id = r.id AND d.delivered_at IS NULL AND d.sent_at IS NULL AND d.failed_at IS NULL
		AND NOT ((d.held_for IS NOT NULL AND EXISTS (SELECT 1 FROM questions hq WHERE hq.id = d.held_for AND hq.status = 'open'))
	OR (d.deliver_after IS NOT NULL AND d.deliver_after > now()))
		AND d.created_at <= COALESCE(hold.cutoff, 'infinity')
		AND (d.claimed_at IS NULL OR d.claimed_at > now() - make_interval(secs => 120)))
	OR EXISTS (SELECT 1 FROM directives d WHERE d.run_id = r.id AND d.failed_at IS NULL AND d.interrupt_only IS NOT TRUE
		AND (d.delivered_at IS NULL OR d.delivered_at > r.waiting_since)
		AND d.created_at <= COALESCE(hold.cutoff, 'infinity')
		AND d.sent_at > now() - make_interval(secs => 120))) AND COALESCE(GREATEST(LEAST((SELECT min(q.asked_at) FROM questions q WHERE q.run_id = r.id AND q.status = 'open'),
	(SELECT min(q.created_at) FROM repository_requests q WHERE q.run_id = r.id AND q.status = 'pending' AND q.blocking),
	(SELECT min((w.state->'escalation'->>'raisedAt')::timestamptz) FROM workflow_runs w WHERE w.task_id = r.task_id AND w.workflow_type = 'task.delivery'
	AND w.status = 'waiting' AND w.state->'escalation'->>'reason' = 'checks_unverified'
	AND w.state->'escalation'->'detail'->>'runId' = r.id AND NOT (w.state->'escalation' ? 'decided')),
	(SELECT min(pw.since) FROM peer_waits pw WHERE pw.run_id = r.id AND pw.closed_at IS NULL),
	(SELECT min(a.occurred_at) FROM events a WHERE a.run_id = r.id AND a.event_type = 'run.checks.asked'
	AND (a.payload->>'deadline')::timestamptz > now()
	AND (a.payload->>'turn')::bigint = (SELECT COALESCE(max(t.cursor), 0) FROM events t WHERE t.run_id = r.id AND t.event_type = 'agent.session.stopped')
	AND NOT EXISTS (SELECT 1 FROM events v WHERE v.run_id = r.id AND v.event_type IN ('run.checks.conducted', 'run.checks.judged')
		AND v.payload->>'turn' = a.payload->>'turn'))),
	(SELECT max(rr.requested_at) FROM run_resumes rr WHERE rr.run_id = r.id),
	(SELECT max(u.occurred_at) FROM events u WHERE u.run_id = r.id AND u.event_type = 'run.unparked')) > now() - make_interval(secs => 2 * lim.park_secs), false)) THEN 'hold' ELSE 'park' END
		ELSE 'none' END,
	COALESCE((r.status = 'paused' AND (r.control = 'resume' OR r.dude_pause IN ('repository', 'machine')
	OR (r.dude_pause = 'person' AND (NOT ask.open OR (NOT (EXISTS (SELECT 1 FROM questions q WHERE q.run_id = r.id AND q.status = 'open')
	OR EXISTS (SELECT 1 FROM repository_requests q WHERE q.run_id = r.id AND q.status = 'pending' AND q.blocking)
	OR EXISTS (SELECT 1 FROM workflow_runs w WHERE w.task_id = r.task_id AND w.workflow_type = 'task.delivery'
	AND w.status = 'waiting' AND w.state->'escalation'->>'reason' = 'checks_unverified'
	AND w.state->'escalation'->'detail'->>'runId' = r.id AND NOT (w.state->'escalation' ? 'decided'))) AND EXISTS (SELECT 1 FROM directives d WHERE d.run_id = r.id AND d.delivered_at IS NULL AND d.sent_at IS NULL AND d.failed_at IS NULL
	AND NOT ((d.held_for IS NOT NULL AND EXISTS (SELECT 1 FROM questions hq WHERE hq.id = d.held_for AND hq.status = 'open'))
	OR (d.deliver_after IS NOT NULL AND d.deliver_after > now()))))))
	OR (r.dude_pause = 'person' AND NOT (EXISTS (SELECT 1 FROM questions q WHERE q.run_id = r.id AND q.status = 'open')
	OR EXISTS (SELECT 1 FROM repository_requests q WHERE q.run_id = r.id AND q.status = 'pending' AND q.blocking)
	OR EXISTS (SELECT 1 FROM workflow_runs w WHERE w.task_id = r.task_id AND w.workflow_type = 'task.delivery'
	AND w.status = 'waiting' AND w.state->'escalation'->>'reason' = 'checks_unverified'
	AND w.state->'escalation'->'detail'->>'runId' = r.id AND NOT (w.state->'escalation' ? 'decided'))) AND EXISTS (SELECT 1 FROM machine_moves m WHERE m.run_id = r.id AND m.settled_at IS NULL))
	OR (r.dude_pause IN ('conductor', 'session') AND EXISTS (SELECT 1 FROM directives d WHERE d.run_id = r.id AND d.delivered_at IS NULL AND d.sent_at IS NULL AND d.failed_at IS NULL
	AND NOT ((d.held_for IS NOT NULL AND EXISTS (SELECT 1 FROM questions hq WHERE hq.id = d.held_for AND hq.status = 'open'))
	OR (d.deliver_after IS NOT NULL AND d.deliver_after > now())))))), false),
	COALESCE(lim.idle_secs > 0 AND (r.status = 'running' AND r.lux_state = 'running' AND r.agent_busy_at IS NOT NULL
	AND r.turn_done_at IS NULL AND r.waiting_since IS NULL AND cardinality(r.open_tool_calls) = 0
	AND GREATEST(COALESCE(r.agent_active_at, r.agent_busy_at), r.idle_nudged_at) < now() - make_interval(secs => lim.idle_secs)
	-- Waiting out a retry's back-off is not going quiet.
	AND NOT EXISTS (SELECT 1 FROM directives rd WHERE rd.run_id = r.id AND rd.deliver_after IS NOT NULL AND rd.failed_at IS NULL AND rd.sent_at IS NULL)), false), r.idle_nudged_at IS NOT NULL, GREATEST(COALESCE(r.agent_active_at, r.agent_busy_at), r.idle_nudged_at),
	r.push_result, r.pr_feedback, r.finding_ids, r.blocking_severities, r.attempt,
	COALESCE(r.turn_done_at < now() - make_interval(secs => lim.warm_secs), false),
	r.keep, COALESCE(r.kept_until <= now(), false), r.finishes, COALESCE(r.image_build_id, ''), EXISTS (SELECT 1 FROM machine_moves m WHERE m.run_id = r.id AND m.settled_at IS NULL),
	r.harness_state->'brokenTurn', COALESCE(extract(epoch FROM now() - (r.harness_state->'brokenTurn'->>'at')::timestamptz)::float8, 0),
	r.retry_failures, r.retry_due_at, COALESCE(r.source_kind, ''), (r.harness_state ? 'timedOut') FROM runs r LEFT JOIN projects p ON p.id = r.project_id JOIN organizations o ON o.id = r.organization_id
	CROSS JOIN LATERAL (SELECT
		COALESCE((p.delivery_policy->>'parkAfterMinutes')::float8 * 60, (o.delivery_policy->>'parkAfterMinutes')::float8 * 60,
			$1::float8) AS park_secs,
		COALESCE((p.delivery_policy->>'idleNudgeMinutes')::float8 * 60, (o.delivery_policy->>'idleNudgeMinutes')::float8 * 60,
			$2::float8) AS idle_secs,
		COALESCE((p.delivery_policy->>'conductorWarmMinutes')::float8 * 60, (o.delivery_policy->>'conductorWarmMinutes')::float8 * 60,
			$3::float8) AS warm_secs) lim
	CROSS JOIN LATERAL (SELECT ((EXISTS (SELECT 1 FROM questions q WHERE q.run_id = r.id AND q.status = 'open')
	OR EXISTS (SELECT 1 FROM repository_requests q WHERE q.run_id = r.id AND q.status = 'pending' AND q.blocking)
	OR EXISTS (SELECT 1 FROM workflow_runs w WHERE w.task_id = r.task_id AND w.workflow_type = 'task.delivery'
	AND w.status = 'waiting' AND w.state->'escalation'->>'reason' = 'checks_unverified'
	AND w.state->'escalation'->'detail'->>'runId' = r.id AND NOT (w.state->'escalation' ? 'decided'))) OR EXISTS (SELECT 1 FROM peer_waits pw WHERE pw.run_id = r.id AND pw.closed_at IS NULL) OR EXISTS (SELECT 1 FROM events a WHERE a.run_id = r.id AND a.event_type = 'run.checks.asked'
	AND (a.payload->>'deadline')::timestamptz > now()
	AND (a.payload->>'turn')::bigint = (SELECT COALESCE(max(t.cursor), 0) FROM events t WHERE t.run_id = r.id AND t.event_type = 'agent.session.stopped')
	AND NOT EXISTS (SELECT 1 FROM events v WHERE v.run_id = r.id AND v.event_type IN ('run.checks.conducted', 'run.checks.judged')
		AND v.payload->>'turn' = a.payload->>'turn'))) AS open) ask
	CROSS JOIN LATERAL (SELECT h.held, CASE WHEN h.held AND (EXISTS (SELECT 1 FROM questions q WHERE q.run_id = r.id AND q.status = 'open')
	OR EXISTS (SELECT 1 FROM repository_requests q WHERE q.run_id = r.id AND q.status = 'pending' AND q.blocking)
	OR EXISTS (SELECT 1 FROM workflow_runs w WHERE w.task_id = r.task_id AND w.workflow_type = 'task.delivery'
	AND w.status = 'waiting' AND w.state->'escalation'->>'reason' = 'checks_unverified'
	AND w.state->'escalation'->'detail'->>'runId' = r.id AND NOT (w.state->'escalation' ? 'decided'))) THEN r.news_hold_at END AS cutoff
		FROM (SELECT CASE WHEN r.news_hold_at IS NULL OR NOT ask.open THEN false ELSE CASE WHEN r.news_hold_at IS NULL THEN false
		ELSE COALESCE(r.news_hold_at >= GREATEST(LEAST((SELECT min(q.asked_at) FROM questions q WHERE q.run_id = r.id AND q.status = 'open'),
	(SELECT min(q.created_at) FROM repository_requests q WHERE q.run_id = r.id AND q.status = 'pending' AND q.blocking),
	(SELECT min((w.state->'escalation'->>'raisedAt')::timestamptz) FROM workflow_runs w WHERE w.task_id = r.task_id AND w.workflow_type = 'task.delivery'
	AND w.status = 'waiting' AND w.state->'escalation'->>'reason' = 'checks_unverified'
	AND w.state->'escalation'->'detail'->>'runId' = r.id AND NOT (w.state->'escalation' ? 'decided')),
	(SELECT min(pw.since) FROM peer_waits pw WHERE pw.run_id = r.id AND pw.closed_at IS NULL),
	(SELECT min(a.occurred_at) FROM events a WHERE a.run_id = r.id AND a.event_type = 'run.checks.asked'
	AND (a.payload->>'deadline')::timestamptz > now()
	AND (a.payload->>'turn')::bigint = (SELECT COALESCE(max(t.cursor), 0) FROM events t WHERE t.run_id = r.id AND t.event_type = 'agent.session.stopped')
	AND NOT EXISTS (SELECT 1 FROM events v WHERE v.run_id = r.id AND v.event_type IN ('run.checks.conducted', 'run.checks.judged')
		AND v.payload->>'turn' = a.payload->>'turn'))),
	(SELECT max(rr.requested_at) FROM run_resumes rr WHERE rr.run_id = r.id),
	(SELECT max(u.occurred_at) FROM events u WHERE u.run_id = r.id AND u.event_type = 'run.unparked')), false) END END AS held) h) hold
			WHERE (r.phase IS NOT NULL OR (r.role IN ('conductor', 'brainstorm') AND r.kind = 'agent'))
			  AND (r.status IN ('pending', 'scheduled', 'starting', 'running')
			       OR (r.status = 'paused' AND (r.control = 'resume' OR r.dude_pause IN ('repository', 'machine')
	OR (r.dude_pause = 'person' AND (NOT ask.open OR (NOT (EXISTS (SELECT 1 FROM questions q WHERE q.run_id = r.id AND q.status = 'open')
	OR EXISTS (SELECT 1 FROM repository_requests q WHERE q.run_id = r.id AND q.status = 'pending' AND q.blocking)
	OR EXISTS (SELECT 1 FROM workflow_runs w WHERE w.task_id = r.task_id AND w.workflow_type = 'task.delivery'
	AND w.status = 'waiting' AND w.state->'escalation'->>'reason' = 'checks_unverified'
	AND w.state->'escalation'->'detail'->>'runId' = r.id AND NOT (w.state->'escalation' ? 'decided'))) AND EXISTS (SELECT 1 FROM directives d WHERE d.run_id = r.id AND d.delivered_at IS NULL AND d.sent_at IS NULL AND d.failed_at IS NULL
	AND NOT ((d.held_for IS NOT NULL AND EXISTS (SELECT 1 FROM questions hq WHERE hq.id = d.held_for AND hq.status = 'open'))
	OR (d.deliver_after IS NOT NULL AND d.deliver_after > now()))))))
	OR (r.dude_pause = 'person' AND NOT (EXISTS (SELECT 1 FROM questions q WHERE q.run_id = r.id AND q.status = 'open')
	OR EXISTS (SELECT 1 FROM repository_requests q WHERE q.run_id = r.id AND q.status = 'pending' AND q.blocking)
	OR EXISTS (SELECT 1 FROM workflow_runs w WHERE w.task_id = r.task_id AND w.workflow_type = 'task.delivery'
	AND w.status = 'waiting' AND w.state->'escalation'->>'reason' = 'checks_unverified'
	AND w.state->'escalation'->'detail'->>'runId' = r.id AND NOT (w.state->'escalation' ? 'decided'))) AND EXISTS (SELECT 1 FROM machine_moves m WHERE m.run_id = r.id AND m.settled_at IS NULL))
	OR (r.dude_pause IN ('conductor', 'session') AND EXISTS (SELECT 1 FROM directives d WHERE d.run_id = r.id AND d.delivered_at IS NULL AND d.sent_at IS NULL AND d.failed_at IS NULL
	AND NOT ((d.held_for IS NOT NULL AND EXISTS (SELECT 1 FROM questions hq WHERE hq.id = d.held_for AND hq.status = 'open'))
	OR (d.deliver_after IS NOT NULL AND d.deliver_after > now()))))))
			       OR (r.control = 'abort' AND r.status IN ('paused', 'aborted'))
			       -- Aborted or failed in dude, and not yet kept or cancelled in
			       -- lux; or kept, and its time is up.
			       OR (r.status IN ('aborted', 'failed') AND r.lux_run_id IS NOT NULL
			           AND r.lux_stop_reason IS DISTINCT FROM 'cancel'
			           AND (r.lux_stop_reason IS DISTINCT FROM 'kept' OR r.kept_until <= now())))
			  -- An abort is due through any back-off. Consuming it clears a live
			  -- Run's back-off and keeps a terminal Run's for its cleanup retries.
			  AND (r.control = 'abort' OR r.next_attempt_at IS NULL
			       OR r.next_attempt_at <= now() + make_interval(secs => $4::float8))
			ORDER BY (r.status = 'pending' OR r.control <> 'none' OR (r.status = 'paused' AND (r.control = 'resume' OR r.dude_pause IN ('repository', 'machine')
	OR (r.dude_pause = 'person' AND (NOT ask.open OR (NOT (EXISTS (SELECT 1 FROM questions q WHERE q.run_id = r.id AND q.status = 'open')
	OR EXISTS (SELECT 1 FROM repository_requests q WHERE q.run_id = r.id AND q.status = 'pending' AND q.blocking)
	OR EXISTS (SELECT 1 FROM workflow_runs w WHERE w.task_id = r.task_id AND w.workflow_type = 'task.delivery'
	AND w.status = 'waiting' AND w.state->'escalation'->>'reason' = 'checks_unverified'
	AND w.state->'escalation'->'detail'->>'runId' = r.id AND NOT (w.state->'escalation' ? 'decided'))) AND EXISTS (SELECT 1 FROM directives d WHERE d.run_id = r.id AND d.delivered_at IS NULL AND d.sent_at IS NULL AND d.failed_at IS NULL
	AND NOT ((d.held_for IS NOT NULL AND EXISTS (SELECT 1 FROM questions hq WHERE hq.id = d.held_for AND hq.status = 'open'))
	OR (d.deliver_after IS NOT NULL AND d.deliver_after > now()))))))
	OR (r.dude_pause = 'person' AND NOT (EXISTS (SELECT 1 FROM questions q WHERE q.run_id = r.id AND q.status = 'open')
	OR EXISTS (SELECT 1 FROM repository_requests q WHERE q.run_id = r.id AND q.status = 'pending' AND q.blocking)
	OR EXISTS (SELECT 1 FROM workflow_runs w WHERE w.task_id = r.task_id AND w.workflow_type = 'task.delivery'
	AND w.status = 'waiting' AND w.state->'escalation'->>'reason' = 'checks_unverified'
	AND w.state->'escalation'->'detail'->>'runId' = r.id AND NOT (w.state->'escalation' ? 'decided'))) AND EXISTS (SELECT 1 FROM machine_moves m WHERE m.run_id = r.id AND m.settled_at IS NULL))
	OR (r.dude_pause IN ('conductor', 'session') AND EXISTS (SELECT 1 FROM directives d WHERE d.run_id = r.id AND d.delivered_at IS NULL AND d.sent_at IS NULL AND d.failed_at IS NULL
	AND NOT ((d.held_for IS NOT NULL AND EXISTS (SELECT 1 FROM questions hq WHERE hq.id = d.held_for AND hq.status = 'open'))
	OR (d.deliver_after IS NOT NULL AND d.deliver_after > now()))))))
	OR (r.turn_done_at IS NOT NULL AND ((r.phase IS NOT NULL AND NOT (r.status IN ('scheduled', 'starting', 'running') AND COALESCE(r.push_branch, '') <> ''
	AND r.push_result IS NULL AND COALESCE(r.push_request_id, '') = '' AND EXISTS (SELECT 1 FROM unnest(r.lux_pushes) p WHERE NOT r.base_shas ? p)
	AND r.turn_done_at > now() - make_interval(secs => $6::float8))) OR EXISTS (SELECT 1 FROM directives d WHERE d.run_id = r.id AND d.delivered_at IS NULL AND d.sent_at IS NULL AND d.failed_at IS NULL
	AND NOT ((d.held_for IS NOT NULL AND EXISTS (SELECT 1 FROM questions hq WHERE hq.id = d.held_for AND hq.status = 'open'))
	OR (d.deliver_after IS NOT NULL AND d.deliver_after > now())))
		OR (r.phase IS NULL AND r.turn_done_at < now() - make_interval(secs => lim.warm_secs))
		OR r.lux_state IN ('stopped', 'succeeded', 'failed', 'cancelled', 'terminated', 'lost')))) DESC, r.created_at
			LIMIT $5