-- 账号贡献任务：管理员发布任务，用户提交账号凭证，审核通过后账号进入指定分组。
CREATE TABLE IF NOT EXISTS account_tasks (
    id                  BIGSERIAL PRIMARY KEY,
    name                VARCHAR(100) NOT NULL,
    description         TEXT NOT NULL DEFAULT '',
    enabled             BOOLEAN NOT NULL DEFAULT TRUE,
    platform            VARCHAR(50) NOT NULL,
    account_types       JSONB NOT NULL DEFAULT '[]',
    target_group_ids    JSONB NOT NULL DEFAULT '[]',
    reward_group_id     BIGINT NOT NULL,
    reward_days         INT NOT NULL DEFAULT 30,
    max_per_user        INT NOT NULL DEFAULT 1,
    expected_base_url   VARCHAR(512) NOT NULL DEFAULT '',
    verify_models       BOOLEAN NOT NULL DEFAULT TRUE,
    reject_on_verify_fail BOOLEAN NOT NULL DEFAULT TRUE,
    priority            INT NOT NULL DEFAULT 50,
    concurrency         INT NOT NULL DEFAULT 3,
    rate_multiplier     DECIMAL(10,4),
    sort_order          INT NOT NULL DEFAULT 0,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at          TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at          TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS idx_account_tasks_enabled ON account_tasks(enabled) WHERE deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_account_tasks_deleted_at ON account_tasks(deleted_at);

-- 用户提交记录：账号先以 inactive 落库，审核通过才激活并入组。
CREATE TABLE IF NOT EXISTS account_submissions (
    id                      BIGSERIAL PRIMARY KEY,
    task_id                 BIGINT NOT NULL REFERENCES account_tasks(id),
    user_id                 BIGINT NOT NULL REFERENCES users(id),
    account_id              BIGINT NOT NULL REFERENCES accounts(id),
    status                  VARCHAR(20) NOT NULL DEFAULT 'pending',
    verification_status     VARCHAR(20) NOT NULL DEFAULT 'not_run',
    verification_error      TEXT NOT NULL DEFAULT '',
    model_count             INT NOT NULL DEFAULT 0,
    credential_fingerprint  VARCHAR(64) NOT NULL DEFAULT '',
    reviewed_by             BIGINT REFERENCES users(id),
    reviewed_at             TIMESTAMPTZ,
    review_note             TEXT NOT NULL DEFAULT '',
    reward_subscription_id  BIGINT REFERENCES user_subscriptions(id),
    reward_granted_at       TIMESTAMPTZ,
    created_at              TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_account_submissions_task_status ON account_submissions(task_id, status);
CREATE INDEX IF NOT EXISTS idx_account_submissions_user_id ON account_submissions(user_id);
CREATE INDEX IF NOT EXISTS idx_account_submissions_account_id ON account_submissions(account_id);
-- 同一份凭证只允许存在一条未拒绝的提交，防止重复提交换取奖励。
CREATE UNIQUE INDEX IF NOT EXISTS idx_account_submissions_fingerprint_active
    ON account_submissions(credential_fingerprint)
    WHERE credential_fingerprint <> '' AND status <> 'rejected';

COMMENT ON TABLE account_tasks IS '账号贡献任务：管理员配置可提交的账号类型与奖励';
COMMENT ON COLUMN account_tasks.target_group_ids IS '审核通过后账号进入的分组；空数组表示仅入库不调度';
COMMENT ON COLUMN account_tasks.expected_base_url IS '限定提交的 base_url 主机，如 api.llm.ustc.edu.cn；空表示不限制';
COMMENT ON TABLE account_submissions IS '用户账号提交与审核记录';
COMMENT ON COLUMN account_submissions.credential_fingerprint IS '凭证内容摘要，用于拒绝重复提交';
