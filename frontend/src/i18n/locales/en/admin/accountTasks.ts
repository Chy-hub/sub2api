export default {
  accountTasks: {
    title: 'Account Tasks',
    description: 'Publish account contribution tasks, review submissions, and grant subscription rewards',
    taskList: 'Tasks',
    createTask: 'New Task',
    editTask: 'Edit Task',
    noTasks: 'No tasks created yet',

    colName: 'Task',
    colPlatform: 'Platform',
    colTypes: 'Account types',
    colReward: 'Reward',
    colTargetGroups: 'Groups',
    colEnabled: 'Status',
    colSubmission: 'Submission',
    colTask: 'Task',
    colVerification: 'Verification',
    colAccount: 'Account',
    colCreated: 'Submitted',

    ungroupedHint: 'Stored only, not scheduled',

    pendingSubmissions: 'Pending Submissions',
    noPending: 'No pending submissions',

    fieldName: 'Task name',
    fieldDescription: 'Description',
    fieldPlatform: 'Platform',
    fieldTypes: 'Allowed account types',
    fieldTypesHint: 'Options follow the platform: oauth only where an authorization flow exists, setup-token / Bedrock only on Anthropic, service accounts only on Gemini / Anthropic.',
    fieldRewardGroup: 'Reward subscription group',
    fieldRewardGroupHint: 'Must be a subscription group. Approved users receive a subscription for the configured days.',
    fieldRewardDays: 'Reward days',
    fieldTargetGroups: 'Account groups (optional)',
    fieldTargetGroupsHint: 'Leave empty to store the account without scheduling it. Selected groups are bound on approval.',
    fieldExpectedBaseUrl: 'Required base URL host',
    fieldExpectedBaseUrlHint: 'When set, only base_url values on this host are accepted (e.g. api.llm.ustc.edu.cn). Leave empty for no restriction.',
    fieldMaxPerUser: 'Max submissions per user',
    fieldPriority: 'Scheduling priority',
    fieldConcurrency: 'Account concurrency',
    fieldSortOrder: 'Sort order',
    fieldRateMultiplier: 'Account rate multiplier',
    fieldRateMultiplierPlaceholder: 'Leave empty for no override',
    fieldRateMultiplierHint: 'Billing multiplier applied to contributed accounts on approval. Leave empty to use the group default.',
    fieldVerifyModels: 'Verify credentials on submit (fetch upstream model list)',
    fieldVerifyModelsHint: 'Verification makes a real upstream request. Disable it if the upstream does not support /v1/models.',
    fieldRejectOnVerifyFail: 'Reject submission when verification fails',
    fieldEnabled: 'Task enabled',

    selectPlaceholder: 'Select',

    typesRequired: 'Select at least one account type',
    rewardGroupRequired: 'Select a reward subscription group',
    rateMultiplierInvalid: 'Rate multiplier must be a number greater than or equal to 0',

    saveSuccess: 'Task saved',
    saveFailed: 'Failed to save task',
    deleteConfirm: 'Delete task "{name}"? Existing submissions are kept.',
    deleteSuccess: 'Task deleted',
    deleteFailed: 'Failed to delete task',

    approve: 'Approve',
    reject: 'Reject',
    approveTitle: 'Approve Submission',
    rejectTitle: 'Reject Submission',
    grantReward: 'Also grant subscription reward',
    reviewNote: 'Review note',
    approveSuccess: 'Approved, account activated',
    rejectSuccess: 'Rejected, credentials cleared',
    reviewFailed: 'Review failed',

    testAccount: 'Open account',

    loadFailed: 'Failed to load tasks',
    loadSubmissionsFailed: 'Failed to load pending submissions'
  }
}
