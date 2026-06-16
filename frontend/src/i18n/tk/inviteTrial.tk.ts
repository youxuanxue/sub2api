// TokenKey-only Invite-to-Trial i18n overlay.
//
// Kept OUT of the upstream locale files (locales/en.ts, locales/zh.ts) so those
// stay near-upstream and merge-safe (CLAUDE.md §5). Deep-merged OVER the upstream
// locale by i18n/index.ts via mergeLocaleMessage. Scope: admin.users.inviteTrial.*

type InviteTrialOverlay = {
  admin: { users: { inviteTrial: Record<string, unknown> } }
}

const en: InviteTrialOverlay = {
  admin: {
    users: {
      inviteTrial: {
        button: 'Invite to Trial',
        title: 'Invite to Trial',
        reinvite: 'Invite another like this',
        plan: 'Trial plan',
        usePreset: 'Use a saved preset',
        customPlan: 'Custom (one-off)',
        preset: 'Preset',
        noPresets: 'No saved presets yet',
        group: 'Group',
        selectGroup: 'Select a subscription group',
        validityDays: 'Validity (days)',
        balance: 'Initial balance (USD)',
        concurrency: 'Concurrency',
        rpmLimit: 'RPM limit (0 = unlimited)',
        rate: 'Rate multiplier (blank = group default)',
        recipients: 'Recipients',
        recipientsHint: 'One email per line. Leave blank and use auto-generate for throwaway trial accounts.',
        recipientsPlaceholder: "alice{'@'}example.com\nbob{'@'}example.com",
        autoCount: 'Auto-generate accounts',
        autoCountHint: 'Create this many accounts with generated email + password.',
        issueKey: 'Issue a trial API key for each user',
        savePreset: 'Save as preset',
        savePresetName: 'Preset name',
        submit: 'Create & generate credentials',
        submitting: 'Creating…',
        groupRequired: 'Please pick a subscription group',
        nothingToCreate: 'Add at least one recipient or set an auto-generate count',
        resultsTitle: 'Credentials',
        resultsDone: 'Done — {ok} created, {failed} failed',
        copy: 'Copy',
        copyAll: 'Copy all',
        copied: 'Copied',
        cardCopied: 'Credential card copied',
        allCopied: 'All credential cards copied',
        apiKey: 'API key',
        expiresAt: 'Expires',
        createMore: 'Create more',
        presetSaved: 'Preset saved'
      }
    }
  }
}

const zh: InviteTrialOverlay = {
  admin: {
    users: {
      inviteTrial: {
        button: '邀请试用',
        title: '邀请试用',
        reinvite: '再邀请一个同样的',
        plan: '试用方案',
        usePreset: '使用已存方案',
        customPlan: '自定义（一次性）',
        preset: '方案',
        noPresets: '暂无已存方案',
        group: '分组',
        selectGroup: '选择一个订阅分组',
        validityDays: '有效期（天）',
        balance: '初始额度（美元）',
        concurrency: '并发',
        rpmLimit: 'RPM 限制（0 = 不限）',
        rate: '倍率（留空 = 分组默认）',
        recipients: '收件人',
        recipientsHint: '一行一个邮箱。留空并用「自动生成」可批量创建试用账号。',
        recipientsPlaceholder: "alice{'@'}example.com\nbob{'@'}example.com",
        autoCount: '自动生成账号数',
        autoCountHint: '按此数量创建账号，邮箱与密码自动生成。',
        issueKey: '为每个用户生成试用 API Key',
        savePreset: '保存为方案',
        savePresetName: '方案名称',
        submit: '创建并生成凭证',
        submitting: '创建中…',
        groupRequired: '请选择一个订阅分组',
        nothingToCreate: '请至少填一个收件人或设置自动生成数量',
        resultsTitle: '凭证',
        resultsDone: '完成 — 成功 {ok}，失败 {failed}',
        copy: '复制',
        copyAll: '复制全部',
        copied: '已复制',
        cardCopied: '凭证卡已复制',
        allCopied: '全部凭证卡已复制',
        apiKey: 'API Key',
        expiresAt: '到期',
        createMore: '继续创建',
        presetSaved: '方案已保存'
      }
    }
  }
}

export default { en, zh }
