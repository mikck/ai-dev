import {Fragment, useCallback, useEffect, useRef, useState} from 'react';
import {createRoot} from 'react-dom/client';
import '@astryxdesign/core/reset.css';
import '@astryxdesign/core/astryx.css';
import {Drawer, DrawerHeader} from '@astryxdesign/core/Drawer';
import {Layout, LayoutContent} from '@astryxdesign/core/Layout';
import {Button} from '@astryxdesign/core/Button';
import {Heading} from '@astryxdesign/core/Heading';
import {HStack} from '@astryxdesign/core/HStack';
import {Text} from '@astryxdesign/core/Text';
import {VStack} from '@astryxdesign/core/VStack';
import {Theme} from '@astryxdesign/core/theme';
import {DeliveryDemo} from './DeliveryDemo.jsx';
import {Executors, WorkspaceSettings} from './Workflow.jsx';
import {themes, defaultThemeSlug} from '../astryx-themes.js';
import './console.css';

const states = {
  queued_plan: '等待生成计划', planning: '生成计划中', awaiting_approval: '待审核',
  queued_execute: '等待执行', executing: '执行中', delivered: '已交付',
  failed_plan: '计划失败', failed_execute: '执行失败',
  sop_queued: '等待执行器', sop_running: '节点执行中', sop_review: '等待人工审核',
  sop_failed: '节点执行失败', sop_rejected: '审核驳回',
};

async function request(path, payload) {
  const response = await fetch(path, payload === undefined ? {} : {
    method: 'POST', headers: {'Content-Type': 'application/json'}, body: JSON.stringify(payload),
  });
  const result = await response.json();
  if (!response.ok) throw new Error(result.error || '请求失败');
  return result;
}

function Artifact({artifact, repositories, nodes}) {
  const {files = [], output, commits, reviews, decision, note, return_to: returnTo, error, ...metadata} = artifact;
  const repoName = id => repositories.find(repo => repo.id === id)?.url.split('/').at(-1).replace(/\.git$/, '') || id;
  const decisionLabels = {approved: '已通过', rejected: '已终止', returned: '已退回'};
  return <VStack gap={2}>
    {output && <pre className="console-output">{output}</pre>}
    {error && <p className="console-error">{error}</p>}
    {(decision || note) && <p>{[decisionLabels[decision] || decision, note].filter(Boolean).join(' · ')}</p>}
    {returnTo && <Text type="supporting">退回节点：{nodes.find(node => node.id === returnTo)?.name ?? returnTo}</Text>}
    {files.map(file => <details key={file.repository + '/' + file.path}>
      <summary>{file.path} · {file.repository}</summary>
      <pre className="console-output">{file.content}</pre>
    </details>)}
    {Object.keys(commits || {}).length > 0 && <dl className="console-commits">
      {Object.entries(commits).map(([id, sha]) => <Fragment key={id}>
        <dt>{repoName(id)}</dt>
        <dd><code title={sha}>{sha.slice(0, 12)}</code></dd>
      </Fragment>)}
    </dl>}
    {Object.keys(reviews || {}).length > 0 && <ul className="console-repo-results">
      {Object.entries(reviews).map(([id, link]) => <li key={id}>
        {repoName(id)}{' '}
        {/^(https:\/\/(github|gitlab)\.com\/)/.test(link)
          ? <a href={link} target="_blank" rel="noreferrer">查看评审</a> : link}
      </li>)}
    </ul>}
    {Object.keys(metadata).length > 0 && <details>
      <summary>其他执行信息</summary>
      <pre className="console-output">{JSON.stringify(metadata, null, 2)}</pre>
    </details>}
  </VStack>;
}

function ReviewForm({detail, pending, onReview}) {
  return <form onSubmit={event => {
    event.preventDefault();
    const fields = new FormData(event.currentTarget, event.nativeEvent.submitter);
    const data = Object.fromEntries(fields);
    if (data.decision === 'returned' && (!data.target || !data.note.trim())) {
      onReview(null);
      return;
    }
    onReview({...data, node: detail.review_node});
  }}>
    <VStack gap={2}>
      <label>审核意见 <textarea name="note" /></label>
      {detail.return_options?.length > 0 && <label>返工节点 <select name="target">
        <option value="">选择上游 Agent</option>
        {detail.return_options.map(node => <option key={node.id} value={node.id}>{node.name}</option>)}
      </select></label>}
      <HStack gap={2} wrap="wrap">
        <Button label="通过" name="decision" value="approved" type="submit" variant="primary" isDisabled={pending} />
        {detail.return_options?.length > 0 && <Button label="退回返工" name="decision" value="returned"
          type="submit" isDisabled={pending} />}
        <Button label="驳回并终止" name="decision" value="rejected" type="submit" isDisabled={pending} />
      </HStack>
    </VStack>
  </form>;
}

const navigation = [
  {id: 'requirements', label: '需求与仓库'},
  {id: 'reviews', label: '待审核', needsWorkspace: true},
  {id: 'settings', label: 'Workspace 配置', needsWorkspace: true},
  {id: 'executors', label: '执行器'},
  {id: 'demo', label: '观看演示'},
];

const nodeStates = {
  pending: '等待依赖', ready: '已排队', running: '执行中', review: '待审核',
  done: '已完成', rejected: '已驳回', failed: '失败',
};

function Status({value, labels = states}) {
  const tone = /failed|rejected/.test(value) ? 'error'
    : /review|approval/.test(value) ? 'review'
    : /delivered|done/.test(value) ? 'success'
    : /running|planning|executing/.test(value) ? 'active' : 'neutral';
  return <strong className="console-status" data-tone={tone}>{labels[value] || value}</strong>;
}

function Requirements({workspace, repositories, requirements, publishedSOPs, pending,
  form, setForm, opened, setOpened, submit, openSettings}) {
  return <>
    {(!workspace || form === 'workspace') && <section className="console-panel console-section">
      <form onSubmit={event => submit(event, '/api/workspaces')}>
        <VStack gap={3}>
          <Heading level={3}>创建 Workspace</Heading>
          <label>Workspace 名称 <input name="name" autoFocus={form === 'workspace'} required /></label>
          <HStack gap={2} wrap="wrap">
            <Button label="创建 workspace" variant="primary" type="submit" isDisabled={pending} />
            {workspace && <Button label="取消" onClick={() => setForm('')} />}
          </HStack>
        </VStack>
      </form>
    </section>}
    {workspace && <>
      {form === 'requirement' && <section className="console-panel console-section">
        <form key={workspace} onSubmit={event => submit(event, '/api/requirements', {workspace})}>
          <VStack gap={3}>
            <Heading level={3}>新建需求</Heading>
            <label>标题 <input name="title" autoFocus required /></label>
            <label>需求说明 <textarea name="description" rows={3} required /></label>
            <input type="hidden" name="engine" value="codex" />
            <label>SOP <select name="sop" defaultValue="" required>
              <option value="">选择已发布 SOP</option>
              {publishedSOPs.map(sop => <option key={sop.id} value={sop.id}>
                {sop.published_name} · v{sop.published_version}
              </option>)}
            </select></label>
            <fieldset><legend>关联仓库</legend>
              <VStack gap={1}>{repositories.map(repo => <label key={repo.id}>
                <input type="checkbox" name="repositories" value={repo.id} /> {repo.url}
              </label>)}</VStack>
            </fieldset>
            <HStack gap={2} wrap="wrap">
              <Button label="提交需求" variant="primary" type="submit" isDisabled={pending} />
              <Button label="取消" onClick={() => setForm('')} />
            </HStack>
          </VStack>
        </form>
      </section>}
      <section className="console-panel" aria-label="需求列表">
        <header className="console-panel-heading"><Heading level={3}>需求列表</Heading></header>
        {requirements.length > 0 ? <table className="console-table">
          <thead><tr><th scope="col">需求</th><th scope="col">SOP 或执行工具</th><th scope="col">状态</th></tr></thead>
          <tbody>{requirements.map(item => <tr key={item.id} data-selected={opened === item.id}>
            <td><Button label={item.title} variant="ghost" onClick={() => setOpened(item.id)} /></td>
            <td data-label="SOP 或执行工具">{item.sop || (item.engine === 'claude' ? 'Claude Code' : 'Codex')}</td>
            <td data-label="状态"><Status value={item.status} /></td>
          </tr>)}</tbody>
        </table> : <p className="console-empty">{!repositories.length ? '登记仓库后可提交需求。'
          : !publishedSOPs.length ? '在 Workspace 配置中发布 SOP 后可提交需求。' : '暂无需求。'}</p>}
      </section>
      <section className="console-panel" aria-label="代码仓库">
        <header className="console-panel-heading">
          <Heading level={3}>代码仓库</Heading>
          <Button label="管理仓库" size="sm" onClick={openSettings} />
        </header>
        {repositories.length > 0 ? <table className="console-table">
          <thead><tr><th scope="col">仓库地址</th><th scope="col">平台</th><th scope="col">目标分支</th></tr></thead>
          <tbody>{repositories.map(repo => <tr key={repo.id}>
            <td><a href={repo.url.replace(/\.git$/, '')} target="_blank" rel="noreferrer">
              {repo.url.replace(/^https:\/\/(github|gitlab)\.com\//, '').replace(/\.git$/, '')}
            </a></td>
            <td data-label="平台">{repo.provider === 'github' ? 'GitHub' : 'GitLab'}</td>
            <td data-label="目标分支"><code>{repo.base}</code></td>
          </tr>)}</tbody>
        </table> : <p className="console-empty">暂无仓库。</p>}
      </section>
    </>}
  </>;
}

const attemptStates = {done: '完成', failed: '失败', returned: '退回返工', rejected: '终止'};

function RunNodes({nodes, repositories}) {
  const [selected, setSelected] = useState('');
  const [isOpen, setIsOpen] = useState(false);
  const node = nodes.find(item => item.id === selected);

  return <VStack gap={2}>
    <Heading level={3}>SOP 节点</Heading>
    <ol className="console-timeline">
      {nodes.map(item => <li key={item.id}>
        <HStack gap={2} wrap="wrap" vAlign="center">
          <Button label={item.name} variant="ghost" aria-haspopup="dialog"
            onClick={() => {setSelected(item.id); setIsOpen(true);}} />
          <Status value={item.status} labels={nodeStates} />
        </HStack>
      </li>)}
    </ol>
    <Drawer isOpen={isOpen && Boolean(node)} onOpenChange={setIsOpen} label="节点详情"
      width="32rem" experimental={{isFullWidthOnMobile: true}}>
      {node && <Layout key={node.id}
        header={<DrawerHeader title={node.name} onOpenChange={setIsOpen} />}
        content={<LayoutContent isScrollable>
          <VStack gap={3}>
            <Status value={node.status} labels={nodeStates} />
            {node.prompt && <section>
              <Heading level={4}>节点指令</Heading>
              <p className="console-output">{node.prompt}</p>
            </section>}
            <section>
              <Heading level={4}>执行记录</Heading>
              {node.attempts?.length > 0 ? (
                <ol className="console-timeline" reversed>
                  {node.attempts.map((attempt, index) => <li key={attempt.id}>
                    <details open={index === 0}>
                      <summary>第 {node.attempts.length - index} 次 · {attemptStates[attempt.status] || attempt.status}</summary>
                      <Artifact artifact={attempt.artifact ?? {}} repositories={repositories} nodes={nodes} />
                    </details>
                  </li>)}
                </ol>
              ) : node.artifact ? (
                <Artifact artifact={node.artifact} repositories={repositories} nodes={nodes} />
              ) : <Text type="supporting">暂无执行记录</Text>}
            </section>
          </VStack>
        </LayoutContent>}
      />}
    </Drawer>
  </VStack>;
}

function RequirementDetail({detail, pending, mutate, setError, onClose}) {
  const panel = useRef(null);
  useEffect(() => {
    if (window.matchMedia('(max-width: 68rem)').matches) {
      panel.current?.focus();
      panel.current?.scrollIntoView({block: 'start'});
    }
  }, [detail.id]);
  return (
      <aside className="console-panel console-section console-detail" aria-label="需求详情" tabIndex={-1} ref={panel}>
        <VStack gap={3}>
          <HStack gap={2} vAlign="center" wrap="wrap">
            <Heading level={2}>{detail.title}</Heading>
            <Button label="关闭详情" variant="ghost" onClick={onClose} />
          </HStack>
          <Status value={detail.status} />
          <Text>{detail.description}</Text>
          {detail.sop && <VStack gap={2}>
            <Heading level={3}>{detail.sop}{detail.sop_version ? ` · v${detail.sop_version}` : ''}</Heading>
            <RunNodes key={detail.id} nodes={detail.nodes} repositories={detail.targets} />
            {detail.status === 'sop_review' && <ReviewForm key={detail.id + '/' + detail.review_node} detail={detail} pending={pending}
              onReview={payload => payload
                ? mutate('/api/requirements/' + detail.id + '/review', payload)
                : setError('请选择返工节点并填写审核意见')} />}
          </VStack>}
          {detail.plan && <section><Heading level={3}>AI 计划</Heading>
            <pre className="console-output">{detail.plan}</pre>
          </section>}
          {detail.log && <section><Heading level={3}>执行结果</Heading>
            <pre className="console-output">{detail.log}</pre>
          </section>}
          <Heading level={3}>仓库与交付结果</Heading>
          <ul className="console-repo-results">{detail.targets.map(repo => (
            <li key={repo.id}>{repo.url}{' '}
              {repo.review ? <a href={repo.review} target="_blank" rel="noreferrer">
                {repo.provider === 'github' ? '查看 PR' : '查看 MR'}
              </a> : '待发布'}
            </li>
          ))}</ul>
          {detail.status === 'awaiting_approval' && <Button label="批准并执行" variant="primary"
            isDisabled={pending} onClick={() => mutate('/api/requirements/' + detail.id + '/approve', {})} />}
          {(detail.status.startsWith('failed_') || detail.status === 'sop_failed') && <Button label="重试" isDisabled={pending}
            onClick={() => mutate('/api/requirements/' + detail.id + '/retry', {})} />}
        </VStack>
      </aside>
  );
}

function Console() {
  const [data, setData] = useState({workspaces: [], repositories: [], requirements: []});
  const [selected, setSelected] = useState('');
  const [opened, setOpened] = useState('');
  const [detail, setDetail] = useState(null);
  const [error, setError] = useState('');
  const [pending, setPending] = useState(false);
  const [tab, setTab] = useState('requirements');
  const [form, setForm] = useState('');
  const [workspaceData, setWorkspaceData] = useState(null);
  const [executors, setExecutors] = useState([]);
  const [reviewData, setReviewData] = useState(null);
  const workspace = selected || data.workspaces[0]?.id;
  const inbox = reviewData?.workspace === workspace ? reviewData?.inbox ?? [] : [];
  const repositories = data.repositories.filter(repo => repo.workspace === workspace);
  const requirements = data.requirements.filter(item => item.workspace === workspace);
  const setup = workspaceData?.workspace === workspace && workspaceData
    ? workspaceData : {resources: [], sops: []};
  const publishedSOPs = setup.sops.filter(sop => sop.published_version);
  const refreshSetup = useCallback(async (signal) => {
    if (!workspace) return;
    const result = await request('/api/workspaces/' + workspace + '/setup');
    if (!signal?.aborted) setWorkspaceData({workspace, ...result});
  }, [workspace]);
  useEffect(() => {
    const controller = new AbortController();
    refreshSetup(controller.signal).catch(exception => {
      if (!controller.signal.aborted) setError(exception.message);
    });
    return () => controller.abort();
  }, [refreshSetup]);

  useEffect(() => {
    let active = true;
    async function refresh() {
      try {
        const list = await request('/api');
        const item = opened ? await request('/api/requirements/' + opened) : null;
        const workers = tab === 'executors' ? await request('/api/executors') : [];
        const reviews = tab === 'reviews' && workspace ? await request('/api/workspaces/' + workspace + '/reviews') : [];
        if (active) {
          setData(list);
          setDetail(item);
          setExecutors(workers);
          setReviewData({workspace, inbox: reviews});
        }
      } catch (err) {
        if (active) setError(err.message);
      }
    }
    refresh();
    const timer = setInterval(refresh, 2000);
    return () => {
      active = false;
      clearInterval(timer);
    };
  }, [opened, tab, workspace]);

  async function mutate(path, payload, form) {
    setError('');
    setPending(true);
    try {
      const result = await request(path, payload);
      setData(await request('/api'));
      if (tab === 'executors') setExecutors(await request('/api/executors'));
      if (tab === 'reviews' && workspace) setReviewData({workspace,
        inbox: await request('/api/workspaces/' + workspace + '/reviews')});
      if (form) {
        form.reset();
        setForm('');
      }
      if (path === '/api/workspaces') {
        setSelected(result.id);
        setOpened('');
        setDetail(null);
      } else if (path === '/api/requirements') {
        setOpened(result.id);
        setDetail(await request('/api/requirements/' + result.id));
      } else if (opened) {
        setDetail(await request('/api/requirements/' + opened));
      }
    } catch (err) {
      setError(err.message);
    } finally {
      setPending(false);
    }
  }

  function submit(event, path, extra = {}) {
    event.preventDefault();
    const fields = new FormData(event.currentTarget);
    const payload = {...Object.fromEntries(fields), ...extra};
    if (path === '/api/requirements') payload.repositories = fields.getAll('repositories');
    mutate(path, payload, event.currentTarget);
  }

  const page = navigation.find(entry => entry.id === tab);
  const showDetail = detail?.id === opened && (tab === 'requirements' || tab === 'reviews');

  return (
    <section className="console-shell">
      <aside className="console-sidebar">
        <header className="console-brand">
          <b aria-hidden="true">A</b>
          <Heading level={1}>需求交付平台</Heading>
        </header>
        <nav className="console-navigation" aria-label="控制台导航">
          {navigation.map(entry => <Button key={entry.id} label={entry.label} variant="ghost"
            aria-current={tab === entry.id ? 'page' : undefined}
            isDisabled={entry.needsWorkspace && !workspace}
            onClick={() => setTab(entry.id)} />)}
        </nav>
      </aside>
      <section className="console-main">
        <header className="console-topbar">
          {workspace && <label>Workspace
            <select value={workspace} onChange={event => {
              setSelected(event.target.value);
              setOpened('');
              setDetail(null);
              setForm('');
            }}>
              {data.workspaces.map(entry => <option value={entry.id} key={entry.id}>{entry.name}</option>)}
            </select>
          </label>}
          <Button label="新建 Workspace" size="sm" onClick={() => {
            setTab('requirements');
            setForm(current => current === 'workspace' ? '' : 'workspace');
          }} />
        </header>
        <section className="console-body">
          <header className="console-page-heading">
            <Heading level={2}>{page.label}</Heading>
            {tab === 'requirements' && workspace && <Button label="新建需求" variant="primary"
              aria-expanded={form === 'requirement'} isDisabled={!repositories.length || !publishedSOPs.length}
              onClick={() => setForm(current => current === 'requirement' ? '' : 'requirement')} />}
          </header>
          {tab !== 'demo' && error && <p className="console-error" role="alert">{error}</p>}
          <section className={showDetail ? 'console-columns with-detail' : 'console-columns'}>
            <VStack gap={4} className="console-content">
              {tab === 'requirements' && <Requirements
                workspace={workspace} repositories={repositories} requirements={requirements}
                publishedSOPs={publishedSOPs} pending={pending} form={form} setForm={setForm}
                opened={opened} setOpened={setOpened} submit={submit} openSettings={() => setTab('settings')} />}
              {tab === 'reviews' && <section className="console-panel">
                {inbox.length === 0 ? <p className="console-empty">当前 Workspace 暂无待审核需求</p> :
                  <table className="console-table review-table">
                    <thead><tr><th scope="col">需求</th><th scope="col">SOP</th><th scope="col">审核节点</th></tr></thead>
                    <tbody>{inbox.map(item => <tr key={item.id} data-selected={opened === item.id}>
                      <td><Button label={item.title} variant="ghost" onClick={() => setOpened(item.id)} /></td>
                      <td data-label="SOP">{item.sop}{item.version ? ` · v${item.version}` : ''}</td>
                      <td data-label="审核节点">{item.node_name}</td>
                    </tr>)}</tbody>
                  </table>}
              </section>}
              {tab === 'settings' && workspace && <WorkspaceSettings key={workspace} workspace={workspace}
                setup={setup} repositories={repositories} request={request} refresh={refreshSetup}
                refreshRepositories={async () => setData(await request('/api'))} />}
              {tab === 'demo' && <DeliveryDemo onExit={() => setTab('requirements')} />}
              {tab === 'executors' && <Executors entries={executors} onManage={mutate} />}
            </VStack>
            {showDetail && <RequirementDetail detail={detail} pending={pending} mutate={mutate}
              setError={setError} onClose={() => {setOpened(''); setDetail(null);}} />}
          </section>
        </section>
      </section>
    </section>
  );
}

createRoot(document.getElementById('app')).render(
  <Theme theme={themes[defaultThemeSlug]}><Console /></Theme>
);
