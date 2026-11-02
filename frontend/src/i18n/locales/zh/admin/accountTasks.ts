export default {
  accountTasks: {
    title: '账号任务',
    description: '发布账号贡献任务，审核用户提交并发放订阅奖励',
    taskList: '任务列表',
    createTask: '新建任务',
    editTask: '编辑任务',
    noTasks: '还没有创建任何任务',

    colName: '任务名称',
    colPlatform: '平台',
    colTypes: '账号类型',
    colReward: '奖励',
    colTargetGroups: '入组',
    colEnabled: '状态',
    colSubmission: '提交',
    colTask: '任务',
    colVerification: '校验结果',
    colAccount: '账号',
    colCreated: '提交时间',

    ungroupedHint: '仅入库，不调度',

    pendingSubmissions: '待审核提交',
    noPending: '没有待审核的提交',

    fieldName: '任务名称',
    fieldDescription: '任务说明',
    fieldPlatform: '平台',
    fieldTypes: '允许的账号类型',
    fieldTypesHint: 'apikey 与 reverse 支持提交时自动校验；oauth 需要未过期的 access_token。',
    fieldRewardGroup: '奖励订阅分组',
    fieldRewardGroupHint: '必须是订阅型分组，审核通过后按天数发放订阅。',
    fieldRewardDays: '奖励天数',
    fieldTargetGroups: '账号入组（可留空）',
    fieldTargetGroupsHint: '留空表示账号仅入库、不参与调度；选择后审核通过时自动绑定这些分组。',
    fieldExpectedBaseUrl: '限定上游地址',
    fieldExpectedBaseUrlHint: '填写后只接受该主机的 base_url，例如 USTC 填 api.llm.ustc.edu.cn；留空不限。',
    fieldMaxPerUser: '每人可提交次数',
    fieldPriority: '调度优先级',
    fieldConcurrency: '账号并发',
    fieldSortOrder: '排序',
    fieldVerifyModels: '提交时校验凭证（拉取上游模型列表）',
    fieldVerifyModelsHint: '校验会真实请求上游，用于确认凭证可用。上游不支持 /v1/models 时可关闭。',
    fieldRejectOnVerifyFail: '校验失败时拒绝提交',
    fieldEnabled: '启用任务',

    selectPlaceholder: '请选择',

    typesRequired: '至少选择一种账号类型',
    rewardGroupRequired: '请选择奖励订阅分组',

    saveSuccess: '任务已保存',
    saveFailed: '保存任务失败',
    deleteConfirm: '确定删除任务「{name}」吗？已提交的记录会保留。',
    deleteSuccess: '任务已删除',
    deleteFailed: '删除任务失败',

    approve: '通过',
    reject: '拒绝',
    approveTitle: '审核通过',
    rejectTitle: '审核拒绝',
    grantReward: '同时发放订阅奖励',
    reviewNote: '审核备注',
    approveSuccess: '已通过，账号已激活',
    rejectSuccess: '已拒绝，凭证已清除',
    reviewFailed: '审核失败',

    testAccount: '打开账号',

    loadFailed: '加载任务失败',
    loadSubmissionsFailed: '加载待审提交失败'
  }
}
