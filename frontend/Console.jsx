import {useCallback, useEffect, useState} from 'react';
import {createRoot} from 'react-dom/client';
import '@astryxdesign/core/reset.css';
import '@astryxdesign/core/astryx.css';
import {Button} from '@astryxdesign/core/Button';
import {Heading} from '@astryxdesign/core/Heading';
import {HStack} from '@astryxdesign/core/HStack';
import {Layout, LayoutContent, LayoutHeader, LayoutPanel} from '@astryxdesign/core/Layout';
import {Text} from '@astryxdesign/core/Text';
import {VStack} from '@astryxdesign/core/VStack';
import {Theme} from '@astryxdesign/core/theme';
import {Executors, WorkspaceSettings} from './Workflow.jsx';
import {themes, defaultThemeSlug} from '../astryx-themes.js';

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

function Console() {
  const [data, setData] = useState({workspaces: [], repositories: [], requirements: []});
  const [selected, setSelected] = useState('');
  const [opened, setOpened] = useState('');
  const [detail, setDetail] = useState(null);
  const [error, setError] = useState('');
  const [pending, setPending] = useState(false);
  const [tab, setTab] = useState('requirements');
  const [workspaceData, setWorkspaceData] = useState(null);
  const [executors, setExecutors] = useState([]);
  const workspace = selected || data.workspaces[0]?.id;
  const repositories = data.repositories.filter(repo => repo.workspace === workspace);
  const requirements = data.requirements.filter(item => item.workspace === workspace);
  const setup = workspaceData?.workspace === workspace && workspaceData
    ? workspaceData : {resources: [], sops: []};
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
        if (active) {
          setData(list);
          setDetail(item);
          setExecutors(workers);
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
  }, [opened, tab]);

  async function mutate(path, payload, form) {
    setError('');
    setPending(true);
    try {
      const result = await request(path, payload);
      setData(await request('/api'));
      if (tab === 'executors') setExecutors(await request('/api/executors'));
      form?.reset();
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

  return (
    <Layout header={
      <LayoutHeader hasDivider padding={0}>
        <HStack gap={3} wrap="wrap" padding={4} vAlign="center">
          <Heading level={1}>需求交付平台</Heading>
          {data.workspaces.length > 0 && <label>Workspace{' '}
            <select value={workspace} onChange={event => {
              setSelected(event.target.value);
              setOpened('');
              setDetail(null);
            }}>
              {data.workspaces.map(entry => <option value={entry.id} key={entry.id}>{entry.name}</option>)}
            </select>
          </label>}
        </HStack>
      </LayoutHeader>
    } end={tab === 'requirements' && detail && detail.id === opened && (
      <LayoutPanel padding={4} label="需求详情" role="complementary">
        <VStack gap={3}>
          <HStack gap={2} vAlign="center" wrap="wrap">
            <Heading level={2}>{detail.title}</Heading>
            <Button label="关闭详情" variant="ghost" onClick={() => {setOpened(''); setDetail(null);}} />
          </HStack>
          <Text>{states[detail.status]}</Text>
          <Text>{detail.description}</Text>
          {detail.sop && <VStack gap={2}>
            <Heading level={3}>{detail.sop}</Heading>
            <ol>{detail.nodes.map(node => <li key={node.id}>
              <Text>{node.name} · {{pending: '等待依赖', ready: '已排队', running: '执行中', review: '待审核',
                done: '已完成', rejected: '已驳回', failed: '失败'}[node.status]}</Text>
              {node.prompt && <Text>{node.prompt}</Text>}
              {node.artifact && <details><summary>查看节点产物</summary>
                <pre style={{whiteSpace: 'pre-wrap', overflowWrap: 'anywhere'}}>{JSON.stringify(node.artifact, null, 2)}</pre>
              </details>}
            </li>)}</ol>
            {detail.status === 'sop_review' && <form onSubmit={event => {
              event.preventDefault();
              const fields = new FormData(event.currentTarget, event.nativeEvent.submitter);
              mutate('/api/requirements/' + detail.id + '/review', Object.fromEntries(fields));
            }}>
              <VStack gap={2}>
                <label>审核意见 <textarea name="note" /></label>
                <HStack gap={2}>
                  <Button label="通过" name="decision" value="approved" type="submit" variant="primary" isDisabled={pending} />
                  <Button label="驳回" name="decision" value="rejected" type="submit" isDisabled={pending} />
                </HStack>
              </VStack>
            </form>}
          </VStack>}
          {detail.plan && <section><Heading level={3}>AI 计划</Heading>
            <pre style={{whiteSpace: 'pre-wrap', overflowWrap: 'anywhere'}}>{detail.plan}</pre>
          </section>}
          {detail.log && <section><Heading level={3}>执行结果</Heading>
            <pre style={{whiteSpace: 'pre-wrap', overflowWrap: 'anywhere'}}>{detail.log}</pre>
          </section>}
          <Heading level={3}>仓库与交付结果</Heading>
          <ul>{detail.targets.map(repo => (
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
      </LayoutPanel>
    )}>
      <LayoutContent padding={4} isScrollable>
        <VStack gap={4}>
          <HStack gap={2} wrap="wrap">
            <Button label="需求与仓库" variant={tab === 'requirements' ? 'primary' : 'ghost'} onClick={() => setTab('requirements')} />
            <Button label="Workspace 配置" variant={tab === 'settings' ? 'primary' : 'ghost'}
              isDisabled={!workspace} onClick={() => setTab('settings')} />
            <Button label="执行器" variant={tab === 'executors' ? 'primary' : 'ghost'} onClick={() => setTab('executors')} />
          </HStack>
          {error && <p role="alert">{error}</p>}
          {tab === 'settings' && workspace && <WorkspaceSettings key={workspace} workspace={workspace}
            setup={setup} request={request} refresh={refreshSetup} />}
          {tab === 'executors' && <Executors entries={executors} onManage={mutate} />}
          {tab === 'requirements' && <>
          <form onSubmit={event => submit(event, '/api/workspaces')}>
            <HStack gap={2} vAlign="center" wrap="wrap">
              <label>Workspace 名称 <input name="name" required /></label>
              <Button label="创建 workspace" type="submit" isDisabled={pending} />
            </HStack>
          </form>
          {workspace && <>
            <Heading level={2}>仓库</Heading>
            {repositories.length > 0 && <ul>{repositories.map(repo => (
              <li key={repo.id}>{repo.url} · {repo.base}</li>
            ))}</ul>}
            <form onSubmit={event => submit(event, '/api/repositories', {workspace})}>
              <HStack gap={2} vAlign="center" wrap="wrap">
                <label>平台 <select name="provider"><option value="github">GitHub</option>
                  <option value="gitlab">GitLab</option></select></label>
                <label>HTTPS 地址 <input name="url" type="url" required /></label>
                <label>目标分支 <input name="base" defaultValue="main" required /></label>
                <label>验证命令 <input name="verify" /></label>
                <Button label="登记仓库" type="submit" isDisabled={pending} />
              </HStack>
            </form>
            <Heading level={2}>需求</Heading>
            {requirements.length > 0 && <table>
              <thead><tr><th scope="col">标题</th><th scope="col">执行工具</th><th scope="col">状态</th></tr></thead>
              <tbody>{requirements.map(item => <tr key={item.id}>
                <td><Button label={item.title} variant="ghost" onClick={() => setOpened(item.id)} /></td>
                <td>{item.status.startsWith('sop_') || item.sop ? '按 SOP' : item.engine === 'claude' ? 'Claude Code' : 'Codex'}</td>
                <td>{states[item.status]}</td>
              </tr>)}</tbody>
            </table>}
            {repositories.length > 0 && <form key={workspace}
              onSubmit={event => submit(event, '/api/requirements', {workspace})}>
              <VStack gap={2}>
                <label>标题 <input name="title" required /></label>
                <label>需求说明 <textarea name="description" required /></label>
                <input type="hidden" name="engine" value="codex" />
                <label>SOP <select name="sop" defaultValue="" required>
                  <option value="">{setup.sops.length ? '选择 SOP' : '请先在 Workspace 配置中创建 SOP'}</option>
                  {setup.sops.map(sop => <option key={sop.id} value={sop.id}>{sop.name}</option>)}
                </select></label>
                <fieldset><legend>关联仓库</legend>
                  <VStack gap={1}>{repositories.map(repo => <label key={repo.id}>
                    <input type="checkbox" name="repositories" value={repo.id} /> {repo.url}
                  </label>)}</VStack>
                </fieldset>
                <HStack><Button label="提交需求" variant="primary" type="submit" isDisabled={pending} /></HStack>
              </VStack>
            </form>}
          </>}
          </>}
        </VStack>
      </LayoutContent>
    </Layout>
  );
}

createRoot(document.getElementById('app')).render(
  <Theme theme={themes[defaultThemeSlug]}><Console /></Theme>
);
