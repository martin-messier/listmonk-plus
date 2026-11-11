// listmonk-plus: sequences admin views (list + editor).
import Alpine from 'alpinejs';
import { api, urls } from '../main.js';
import * as u from '../utils.js';

const UNITS = { seconds: 1, minutes: 60, hours: 3600, days: 86400 };

// Split a delay in seconds into the largest whole unit (minutes/hours/days).
function splitDelay(sec) {
  sec = Number(sec) || 0;
  if (sec === 0) return { n: 0, unit: 'minutes' };
  for (const unit of ['days', 'hours', 'minutes']) {
    if (sec % UNITS[unit] === 0) return { n: sec / UNITS[unit], unit };
  }
  return { n: sec, unit: 'seconds' };
}

function plural(n, word) {
  return `${n} ${word.replace(/s$/, '')}${n === 1 ? '' : 's'}`;
}

function stepForm(step) {
  const d = splitDelay(step ? step.delay_seconds : 0);
  return {
    id: step ? step.id : undefined,
    position: step ? step.position : undefined,
    delay_n: d.n,
    delay_unit: d.unit,
    template_id: step ? step.template_id : '',
    subject: step && step.subject ? step.subject : '',
    skip_landing_url: step && step.skip_landing_url ? step.skip_landing_url : '',
  };
}

function stepPayload(f) {
  return {
    delay: `${Math.max(0, Math.floor(Number(f.delay_n) || 0))} ${f.delay_unit}`,
    template_id: Number(f.template_id),
    subject: f.subject || '',
    skip_landing_url: f.skip_landing_url || '',
  };
}

const SKIP_LABELS = {
  not_found: 'no such subscriber',
  not_on_list: 'not on the list',
  unsubscribed: 'unsubscribed',
  unconfirmed: 'not confirmed yet (double opt-in)',
  subscriber_blocklisted: 'blocklisted',
  subscriber_disabled: 'disabled',
  already_active: 'already in this sequence',
  already_completed_or_left: 'already went through it (re-entry is off)',
  in_other_sequence: 'currently in another sequence',
};

// Sequences list page.
function listView() {
  return {
    async setStatus(id, status) {
      await api('plus.status', `/plus/sequences/${id}`, 'PUT', { status });
      u.reload({ message: status === 'active' ? 'Sequence activated' : 'Sequence paused' });
    },

    async onDelete(id, name) {
      if (!(await u.confirm(`Delete "${name}" and its history? This can't be undone.`))) return;
      await api('plus.delete', `/plus/sequences/${id}`, 'DELETE');
      u.reload({ message: `Deleted "${name}"` });
    },
  };
}

// Sequence editor page.
function editorView(seq, opts) {
  return {
    seq,
    opts: opts || { lists: [], templates: [] },
    steps: seq ? seq.steps.map(stepForm) : [],
    newStep: stepForm(null),
    form: {
      name: seq ? seq.name : '',
      list_id: seq ? seq.list_id : '',
      trigger_enabled: seq ? seq.trigger_enabled : true,
      reentry_while_active: seq ? seq.reentry_while_active : 'continue',
      reentry_after_exit: seq ? seq.reentry_after_exit : 'ignore',
      from_email: seq && seq.from_email ? seq.from_email : '',
    },
    enrollForm: { mode: 'all', emails: '', exclude_other: true },
    enrollPreview: null,

    get isActive() {
      return this.seq && this.seq.status === 'active';
    },

    get base() {
      return `${urls.admin}/plus/sequences`;
    },

    waitLabel(step) {
      if (!step.delay_n) return 'sent right after';
      return `waits ${plural(step.delay_n, step.delay_unit)}`;
    },

    skipLabel(reason) {
      return SKIP_LABELS[reason] || reason.replace(/_/g, ' ');
    },

    // Settings.
    async createSequence() {
      const data = await api('plus.save', '/plus/sequences', 'POST', this._settingsPayload());
      u.redirect(`${this.base}/${data.id}`, { message: `Created "${data.name}". Now add its steps.` });
    },

    async saveSettings() {
      const p = this._settingsPayload();
      if (this.seq.status !== 'draft') delete p.list_id;
      const data = await api('plus.save', `/plus/sequences/${this.seq.id}`, 'PUT', p);
      u.reload({ message: `Saved "${data.name}"` });
    },

    async deleteSequence() {
      if (!(await u.confirm(`Delete "${this.seq.name}" and its history? This can't be undone.`))) return;
      await api('plus.delete', `/plus/sequences/${this.seq.id}`, 'DELETE');
      u.redirect(this.base, { message: `Deleted "${this.seq.name}"` });
    },

    async setStatus(status) {
      await api('plus.status', `/plus/sequences/${this.seq.id}`, 'PUT', { status });
      u.reload({ message: status === 'active' ? 'Sequence activated' : 'Sequence paused' });
    },

    // Steps.
    async addStep() {
      await api('plus.step', `/plus/sequences/${this.seq.id}/steps`, 'POST', stepPayload(this.newStep));
      u.reload({ message: 'Step added' });
    },

    async saveStep(step) {
      await api('plus.step', `/plus/sequences/${this.seq.id}/steps/${step.id}`, 'PUT', stepPayload(step));
      u.toast(`Step ${step.position} saved`, 'success');
    },

    async moveStep(step, dir) {
      await api('plus.step', `/plus/sequences/${this.seq.id}/steps/${step.id}`, 'PUT', { position: step.position + dir });
      u.reload();
    },

    async deleteStep(step) {
      if (!(await u.confirm(`Delete step ${step.position}?`))) return;
      await api('plus.step', `/plus/sequences/${this.seq.id}/steps/${step.id}`, 'DELETE');
      u.reload({ message: 'Step deleted' });
    },

    // Enrollment.
    openEnroll() {
      this.enrollPreview = null;
      this.$refs.enrollDialog.showModal();
    },

    get enrollEmails() {
      return this.enrollForm.emails.split(/[\s,;]+/).map((e) => e.trim()).filter(Boolean);
    },

    // People the last preview would enroll (0 until a preview has been run).
    get enrollCount() {
      const p = this.enrollPreview;
      return p && p.dry_run ? p.enrolled + (p.restarted || 0) : 0;
    },

    get enrollReady() {
      return this.enrollForm.mode === 'all' || this.enrollEmails.length > 0;
    },

    async enroll(dryRun) {
      const body = {
        exclude_other_sequences: this.enrollForm.exclude_other,
        dry_run: dryRun,
        source: this.enrollForm.mode === 'all' ? 'manual-bulk' : 'manual',
      };
      if (this.enrollForm.mode === 'all') {
        body.all = true;
      } else {
        body.subscriber_emails = this.enrollEmails;
      }
      this.enrollPreview = await api('plus.enroll', `/plus/sequences/${this.seq.id}/enrollments`, 'POST', body);
      if (!dryRun) {
        const n = this.enrollPreview.enrolled + (this.enrollPreview.restarted || 0);
        this.$refs.enrollDialog.close();
        u.redirect(`${this.base}/${this.seq.id}/activity`, { message: `Enrolled ${n} ${n === 1 ? 'person' : 'people'}` });
      }
    },

    async cancelEnrollment(id, email) {
      if (!(await u.confirm(`Stop the sequence for ${email || 'this person'}? They won't receive the remaining emails.`))) return;
      await api('plus.cancel', `/plus/sequences/${this.seq.id}/enrollments/${id}`, 'DELETE');
      u.reload({ message: 'Stopped' });
    },

    _settingsPayload() {
      return {
        name: this.form.name,
        list_id: Number(this.form.list_id),
        trigger_enabled: !!this.form.trigger_enabled,
        reentry_while_active: this.form.reentry_while_active,
        reentry_after_exit: this.form.reentry_after_exit,
        from_email: this.form.from_email || '',
      };
    },
  };
}

document.addEventListener('alpine:init', () => {
  Alpine.data('plusSequences', listView);
  Alpine.data('plusSequence', editorView);
}, { once: true });
