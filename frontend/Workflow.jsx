import {useEffect, useId, useState} from 'react';
import {Button} from '@astryxdesign/core/Button';
import {Heading} from '@astryxdesign/core/Heading';
import {HStack} from '@astryxdesign/core/HStack';
import {Text} from '@astryxdesign/core/Text';
import {VStack} from '@astryxdesign/core/VStack';
import {SOPCanvas} from './SOPCanvas.jsx';

const resourceLabels = {agent: 'Agent', skill: 'Skill', mcp: 'MCP'};
const categories = {repositories: '仓库', ...resourceLabels, sop: 'SOP'};
const categoryKeys = Object.keys(categories);


export function WorkspaceSettings({workspace, setup, repositories, request, refresh, refreshRepositories}) {
  const tabsId = useId();
  const [category, setCategory] = useState('repositories');
  const [query, setQuery] = useState('');
  const [repository, setRepository] = useState(null);
  const [resource, setResource] = useState(null);
  const [sop, setSop] = useState(null);
  const [draft, setDraft] = useState('');
  const [history, setHistory] = useState([]);
  const [canvasVersion, setCanvasVersion] = useState(0);
  const [error, setError] = useState('');
  const [notice, setNotice] = useState('');
  const [busy, setBusy] = useState(false);
  const agents = setup.resources.filter(row => row.kind === 'agent');
  const search = query.trim().toLowerCase();
  const matches = value => value?.toLowerCase().includes(search);
  const kind = Object.hasOwn(resourceLabels, category) ? category : null;
  const shownResources = setup.resources.filter(row => row.kind === kind && matches(row.name));
  const shownRepositories = repositories.filter(row => matches(row.url) || matches(row.base));
  const shownSOPs = setup.sops.filter(row => matches(row.name) || matches(row.published_name));
  const emptyList = search ? '没有匹配的配置' : '暂无配置';

  function changeCategory(next) {
    if (next === category) return;
    setCategory(next);
    setQuery('');
    setResource(null);
    setRepository(null);
    editSop(null);
  }

  function moveTab(event) {
    const index = categoryKeys.indexOf(category);
    const last = categoryKeys.length - 1;
    const next = {ArrowLeft: (index + last) % categoryKeys.length,
      ArrowRight: (index + 1) % categoryKeys.length, Home: 0, End: last}[event.key];
    if (next === undefined) return;
    event.preventDefault();
    changeCategory(categoryKeys[next]);
    document.getElementById(tabsId + '-' + categoryKeys[next])?.focus();
  }

  async function submit(path, payload, onSuccess) {
    setError('');
    setNotice('');
    setBusy(true);
    try {
      const result = await request(path, {workspace, ...payload});
      await onSuccess(result);
    } catch (exception) {
      setError(exception.message);
    } finally {
      setBusy(false);
    }
  }

  useEffect(() => {
    if (!draft) return;
    let active = true;
    async function checkDraft() {
      try {
        const result = await request('/api/drafts/' + draft);
        if (!active) return;
        if (result.status === 'done') {
          setSop(current => current && ({...current, nodes: result.nodes}));
          setCanvasVersion(version => version + 1);
          setHistory(previous => [...previous, {role: 'assistant', content: JSON.stringify(result.nodes)}]);
          setNotice('AI 已生成草稿，可继续修改或保存。');
          setDraft('');
        } else if (result.status === 'failed') {
          setError(result.error);
          setDraft('');
        }
      } catch (exception) {
        if (active) setError(exception.message);
      }
    }
    const timer = setInterval(checkDraft, 1500);
    return () => {active = false; clearInterval(timer);};
  }, [draft, request]);

  function saveResource(event) {
    event.preventDefault();
    const fields = new FormData(event.currentTarget);
    let config;
    if (resource.kind === 'agent') {
      config = {engine: fields.get('engine'), prompt: fields.get('prompt'),
        skills: fields.getAll('skills'), mcps: fields.getAll('mcps')};
    } else if (resource.kind === 'mcp') {
      config = {command: fields.get('command'), args: fields.get('args').split('\n')
        .map(line => line.trim()).filter(Boolean)};
    } else {
      config = {content: fields.get('content')};
    }
    submit('/api/resources', {identifier: resource.id, kind: resource.kind, name: fields.get('name'), config},
      async () => {setResource(null); await refresh();});
  }

  function editSop(entry) {
    setSop(entry);
    setCanvasVersion(version => version + 1);
    setDraft('');
    setHistory([]);
    setError('');
    setNotice('');
  }

  return (
    <VStack as="section" gap={3}>
      {error && <p className="console-error" role="alert">{error}</p>}
      {notice && <p role="status">{notice}</p>}
      <nav className="console-settings-tabs" role="tablist" aria-label="配置分类" onKeyDown={moveTab}>
        {categoryKeys.map(key => <button key={key} id={tabsId + '-' + key} type="button" role="tab"
          aria-selected={category === key} aria-controls={tabsId + '-panel'}
          tabIndex={category === key ? 0 : -1} onClick={() => changeCategory(key)}>{categories[key]}</button>)}
      </nav>
      <VStack as="section" role="tabpanel" id={tabsId + '-panel'}
        aria-labelledby={tabsId + '-' + category} gap={3}>
      <label className="console-settings-search">搜索{categories[category]}
        <input type="search" value={query} onChange={event => setQuery(event.target.value)} />
      </label>
      {category === 'repositories' && <>
        <VStack as="section" gap={2} className="console-panel console-section">
          <HStack gap={2} wrap="wrap" vAlign="center">
            <Heading level={3}>仓库</Heading>
            <Button label="添加仓库" size="sm" onClick={() => setRepository({provider: 'github', base: 'main'})} />
          </HStack>
          {!shownRepositories.length && <Text type="supporting">{emptyList}</Text>}
          {shownRepositories.map(item => <HStack key={item.id} gap={2} wrap="wrap" vAlign="center" className="console-sop-row">
            <Text>{item.url} · {item.base}</Text>
            <Button label={'编辑 ' + item.url} variant="ghost" size="sm" onClick={() => setRepository(item)}>编辑</Button>
          </HStack>)}
        </VStack>
        {repository && <form className="console-panel console-section" key={repository.id || 'new'}
          onSubmit={event => {
            event.preventDefault();
            const fields = Object.fromEntries(new FormData(event.currentTarget));
            submit('/api/repositories', {...fields, identifier: repository.id}, async () => {
              setRepository(null);
              await refreshRepositories();
            });
          }}>
          <VStack gap={3}>
            <Heading level={3}>{repository.id ? '编辑仓库' : '添加仓库'}</Heading>
            <label>平台 <select name="provider" defaultValue={repository.provider}>
              <option value="github">GitHub</option><option value="gitlab">GitLab</option>
            </select></label>
            <label>HTTPS 地址 <input name="url" type="url" defaultValue={repository.url} required /></label>
            <label>目标分支 <input name="base" defaultValue={repository.base} required /></label>
            <label>验证命令 <input name="verify" defaultValue={repository.verify} /></label>
            <HStack gap={2} wrap="wrap">
              <Button label="保存仓库" type="submit" variant="primary" isDisabled={busy} />
              <Button label="取消" onClick={() => setRepository(null)} />
              {repository.id && <Button label="删除仓库" variant="destructive" isDisabled={busy} onClick={() => {
                if (window.confirm('删除仓库？已有关联需求仍保留原配置。')) {
                  submit('/api/repositories/delete', {identifier: repository.id}, async () => {
                    setRepository(null);
                    await refreshRepositories();
                  });
                }
              }} />}
            </HStack>
          </VStack>
        </form>}
      </>}

      {kind && <VStack as="section" gap={2} className="console-panel console-section">
        <HStack gap={2} wrap="wrap" vAlign="center">
          <Heading level={3}>{resourceLabels[kind]}</Heading>
          <Button label={'添加 ' + resourceLabels[kind]} size="sm" onClick={() => setResource({kind, name: '', config: {}})} />
        </HStack>
        {!shownResources.length && <Text type="supporting">{emptyList}</Text>}
        {shownResources.map(row => <HStack key={row.id} gap={2} wrap="wrap" vAlign="center" className="console-sop-row">
          <Text>{row.name}</Text>
          <Button label={'编辑 ' + row.name} variant="ghost" size="sm" onClick={() => setResource(row)} />
        </HStack>)}
      </VStack>}
      {resource && <form className="console-panel console-section" key={resource.id || resource.kind} onSubmit={saveResource}>
        <VStack gap={2}>
          <Heading level={3}>{resource.id ? '编辑 ' : '添加 '}{resourceLabels[resource.kind]}</Heading>
          <label>名称 <input name="name" defaultValue={resource.name} required /></label>
          {resource.kind === 'agent' && <>
            <label>执行工具 <select name="engine" defaultValue={resource.config.engine || 'codex'}>
              <option value="codex">Codex CLI</option><option value="claude">Claude Code CLI</option>
            </select></label>
            <label>Agent 指令 <textarea name="prompt" defaultValue={resource.config.prompt} /></label>
            {['skill', 'mcp'].map(kind => <fieldset key={kind}>
              <legend>{resourceLabels[kind]}</legend>
              {setup.resources.filter(row => row.kind === kind).map(row => <label key={row.id}>
                <input type="checkbox" name={kind + 's'} value={row.id}
                  defaultChecked={resource.config[kind + 's']?.includes(row.id)} /> {row.name}
              </label>)}
            </fieldset>)}
          </>}
          {resource.kind === 'skill' && <label>Skill 内容 <textarea name="content"
            defaultValue={resource.config.content} required /></label>}
          {resource.kind === 'mcp' && <>
            <label>stdio 命令 <input name="command" defaultValue={resource.config.command} required /></label>
            <label>参数（每行一个） <textarea name="args" defaultValue={resource.config.args?.join('\n')} /></label>
          </>}
          <HStack gap={2} wrap="wrap">
            <Button label="保存资源" type="submit" variant="primary" isDisabled={busy} />
            <Button label="取消" onClick={() => setResource(null)} />
            {resource.id && <Button label="删除资源" variant="destructive" isDisabled={busy}
              onClick={() => {
                if (window.confirm('删除资源？')) submit('/api/resources/delete', {identifier: resource.id},
                  async () => {setResource(null); await refresh();});
              }} />}
          </HStack>
        </VStack>
      </form>}
      {category === 'sop' && <>
      <VStack as="section" gap={2} className="console-panel console-section">
      <HStack gap={2} vAlign="center" wrap="wrap">
        <Heading level={3}>SOP</Heading>
        <Button label="新建 SOP" onClick={() => editSop({name: '', nodes: []})} />
      </HStack>
      {!shownSOPs.length && <Text type="supporting">{emptyList}</Text>}
      {shownSOPs.map(entry => <VStack key={entry.id} gap={1} className="console-sop-row">
        <HStack gap={2} wrap="wrap" vAlign="center">
          <Text>{entry.name} · 草稿{entry.published_version ? ` / 最新发布 ${entry.published_name} v${entry.published_version}` : ''}</Text>
          <Button label={'编辑 ' + entry.name} size="sm" variant="ghost" onClick={() => editSop(entry)} />
          <Button label="发布新版本" size="sm" isDisabled={busy} onClick={() =>
            submit('/api/sops/publish', {identifier: entry.id}, async result => {
              setNotice(`已发布 v${result.version}`);
              await refresh();
            })} />
        </HStack>
        {entry.versions.length > 0 && <details><summary>已发布版本</summary>
          <ol reversed>{entry.versions.map(version => <li key={version.version}>
            v{version.version} · {version.name} · {new Date(version.published * 1000).toLocaleString()}
          </li>)}</ol>
        </details>}
      </VStack>)}
      </VStack>
      {sop && <VStack as="section" gap={3} className="console-panel console-section console-sop-editor">
        <label>SOP 名称 <input value={sop.name} onChange={event => setSop({...sop, name: event.target.value})} /></label>
        <details className="sop-ai-configuration">
          <summary>与 AI 编排 SOP</summary>
        <form onSubmit={event => {
          event.preventDefault();
          const form = event.currentTarget;
          const fields = new FormData(form);
          const messages = [...history, {role: 'user', content: fields.get('message')}];
          submit('/api/drafts', {engine: fields.get('engine'), messages}, result => {
            setHistory(messages);
            setDraft(result.id);
            form.reset();
          });
        }}>
          <VStack gap={2}>
            {history.map((turn, index) => <Text key={index}>
              {turn.role === 'user' ? turn.content : 'AI 已生成编排草稿'}
            </Text>)}
            {draft && <p role="status">等待执行器生成草稿…</p>}
            <label>生成工具 <select name="engine">
              <option value="codex">Codex CLI</option><option value="claude">Claude Code CLI</option>
            </select></label>
            <label>描述流程或提出修改 <textarea name="message" required /></label>
            <HStack><Button label="发送" type="submit" isDisabled={busy || Boolean(draft)} /></HStack>
          </VStack>
        </form>
        </details>
        <SOPCanvas key={canvasVersion} nodes={sop.nodes} agents={agents}
          onChange={nodes => setSop(current => ({...current, nodes}))}>
          <Button label="保存草稿" variant="primary" isDisabled={busy || Boolean(draft)} onClick={() =>
            submit('/api/sops', {identifier: sop.id, name: sop.name, nodes: sop.nodes}, async () => {
              editSop(null);
              await refresh();
            })} />
          <Button label="关闭" onClick={() => editSop(null)} />
          {sop.id && <Button label={sop.published_version ? "删除 SOP" : "删除草稿"} variant="destructive" isDisabled={busy} onClick={() => {
            if (window.confirm('删除 SOP？已有需求仍保留关联版本。')) submit('/api/sops/delete', {identifier: sop.id},
              async () => {editSop(null); await refresh();});
          }} />}
        </SOPCanvas>
      </VStack>}
      </>}
      </VStack>
    </VStack>
  );
}

export function Executors({entries, onManage}) {
  return <VStack as="section" gap={2} className="console-panel console-section">
    {entries.length === 0 && <Text>暂无执行器注册</Text>}
    {entries.map(executor => <HStack key={executor.id} gap={2} wrap="wrap" vAlign="center">
      <Text>{executor.name}（{executor.id}）</Text>
      <strong className="console-status" data-tone={executor.online ? 'success' : 'neutral'}>
        {executor.online ? '在线' : '离线'}
      </strong>
      <Text>{executor.enabled ? '已启用' : '已停用'} · {executor.running} 个执行中</Text>
      <Button label={executor.enabled ? '停用' : '启用'} size="sm" onClick={() =>
        onManage('/api/executors/manage', {identifier: executor.id, enabled: !executor.enabled})} />
      {!executor.online && !executor.enabled && executor.running > 0 && <Button label="恢复任务" size="sm"
        onClick={() => {
          if (window.confirm('确认执行器进程已停止，标记任务失败以便重试？')) {
            onManage('/api/executors/recover', {identifier: executor.id});
          }
        }} />}
    </HStack>)}
  </VStack>;
}
