import {useEffect, useState} from 'react';
import {Button} from '@astryxdesign/core/Button';
import {Heading} from '@astryxdesign/core/Heading';
import {HStack} from '@astryxdesign/core/HStack';
import {Text} from '@astryxdesign/core/Text';
import {VStack} from '@astryxdesign/core/VStack';

const resourceLabels = {agent: 'Agent', skill: 'Skill', mcp: 'MCP'};
const newNode = () => ({id: crypto.randomUUID().replaceAll('-', ''), name: '', type: 'agent',
  agent: '', prompt: '', depends: [], publish: false});

export function WorkspaceSettings({workspace, setup, request, refresh}) {
  const [resource, setResource] = useState(null);
  const [sop, setSop] = useState(null);
  const [dragged, setDragged] = useState(null);
  const [draft, setDraft] = useState('');
  const [history, setHistory] = useState([]);
  const [error, setError] = useState('');
  const [notice, setNotice] = useState('');
  const [busy, setBusy] = useState(false);
  const agents = setup.resources.filter(row => row.kind === 'agent');

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

  function updateNode(id, changes) {
    setSop(current => ({...current, nodes: current.nodes.map(node => node.id === id ? {...node, ...changes} : node)}));
  }

  function moveNode(from, to) {
    setSop(current => {
      const nodes = [...current.nodes];
      nodes.splice(to, 0, nodes.splice(from, 1)[0]);
      return {...current, nodes};
    });
  }

  function editSop(entry) {
    setSop(entry);
    setDraft('');
    setHistory([]);
    setError('');
    setNotice('');
  }

  return (
    <VStack as="section" gap={3}>
      <Heading level={2}>Workspace 配置</Heading>
      {error && <p role="alert">{error}</p>}
      {notice && <p role="status">{notice}</p>}
      {Object.entries(resourceLabels).map(([kind, label]) => (
        <VStack key={kind} gap={2}>
          <HStack gap={2} wrap="wrap" vAlign="center">
            <Heading level={3}>{label}</Heading>
            <Button label={'添加 ' + label} size="sm" onClick={() => setResource({kind, name: '', config: {}})} />
          </HStack>
          {setup.resources.filter(row => row.kind === kind).map(row => (
            <HStack key={row.id} gap={2} wrap="wrap" vAlign="center">
              <Text>{row.name}</Text>
              <Button label={'编辑 ' + row.name} variant="ghost" size="sm" onClick={() => setResource(row)} />
            </HStack>
          ))}
        </VStack>
      ))}
      {resource && <form key={resource.id || resource.kind} onSubmit={saveResource}>
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
      <HStack gap={2} vAlign="center" wrap="wrap">
        <Heading level={3}>SOP</Heading>
        <Button label="新建 SOP" onClick={() => editSop({name: '', nodes: []})} />
      </HStack>
      {setup.sops.map(entry => <VStack key={entry.id} gap={1}>
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
      {sop && <VStack gap={3}>
        <label>SOP 名称 <input value={sop.name} onChange={event => setSop({...sop, name: event.target.value})} /></label>
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
            <Heading level={4}>与 AI 编排 SOP</Heading>
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
        <Text type="supporting">拖动节点标题调整执行优先级，或拖入另一节点的前置节点区域来添加依赖。</Text>
        <ol>
          {sop.nodes.map((node, index) => (
            <li key={node.id} onDragOver={event => event.preventDefault()} onDrop={event => {
              event.preventDefault();
              if (dragged !== null && dragged < sop.nodes.length) moveNode(dragged, index);
              setDragged(null);
            }}>
              <VStack gap={2} paddingBlock={2}>
                <HStack gap={2} wrap="wrap" vAlign="center">
                  <strong draggable onDragStart={event => {
                    event.dataTransfer.setData('text/plain', node.id);
                    setDragged(index);
                  }} onDragEnd={() => setDragged(null)}>{node.name || '未命名节点'}</strong>
                  <Button label="上移" size="sm" isDisabled={index === 0} onClick={() => moveNode(index, index - 1)} />
                  <Button label="下移" size="sm" isDisabled={index === sop.nodes.length - 1} onClick={() => moveNode(index, index + 1)} />
                  <Button label="移除节点" size="sm" variant="ghost" onClick={() => setSop({...sop,
                    nodes: sop.nodes.filter(other => other.id !== node.id).map(other => ({...other,
                      depends: other.depends.filter(id => id !== node.id)}))})} />
                </HStack>
                <label>节点名称 <input value={node.name}
                  onChange={event => updateNode(node.id, {name: event.target.value})} /></label>
                <label>节点类型 <select value={node.type}
                  onChange={event => updateNode(node.id, {type: event.target.value})}>
                  <option value="agent">Agent 执行</option><option value="review">人工审核</option>
                </select></label>
                <label>{node.type === 'agent' ? '执行说明' : '审核说明'} <textarea value={node.prompt}
                  onChange={event => updateNode(node.id, {prompt: event.target.value})} /></label>
                {node.type === 'agent' && <>
                  <label>Agent <select value={node.agent || ''}
                    onChange={event => updateNode(node.id, {agent: event.target.value})}>
                    <option value="">选择 Agent</option>
                    {agents.map(agent => <option key={agent.id} value={agent.id}>{agent.name}</option>)}
                  </select></label>
                  <label>文本产物路径（相对仓库根目录，每行一个）
                    <textarea key={draft || history.length} defaultValue={node.artifacts?.join('\n')}
                      onChange={event => updateNode(node.id,
                        {artifacts: event.target.value.split('\n').map(path => path.trim()).filter(Boolean)})} />
                  </label>
                  <label><input type="checkbox" checked={Boolean(node.publish)}
                    onChange={event => updateNode(node.id, {publish: event.target.checked})} /> 推送分支并创建 PR／MR</label>
                </>}
                <fieldset onDragOver={event => event.preventDefault()} onDrop={event => {
                  event.preventDefault();
                  event.stopPropagation();
                  const source = sop.nodes[dragged];
                  if (source && source.id !== node.id && !node.depends.includes(source.id)) {
                    updateNode(node.id, {depends: [...node.depends, source.id]});
                  }
                  setDragged(null);
                }}><legend>前置节点</legend>
                  {sop.nodes.filter(other => other.id !== node.id).map(other => <label key={other.id}>
                    <input type="checkbox" checked={node.depends.includes(other.id)} onChange={event =>
                      updateNode(node.id, {depends: event.target.checked ? [...node.depends, other.id]
                        : node.depends.filter(id => id !== other.id)})} /> {other.name || '未命名节点'}
                  </label>)}
                </fieldset>
              </VStack>
            </li>
          ))}
        </ol>
        <HStack gap={2} wrap="wrap">
          <Button label="添加节点" onClick={() => setSop({...sop, nodes: [...sop.nodes, newNode()]})} />
          <Button label="保存草稿" variant="primary" isDisabled={busy || Boolean(draft)} onClick={() =>
            submit('/api/sops', {identifier: sop.id, name: sop.name, nodes: sop.nodes}, async () => {
              editSop(null);
              await refresh();
            })} />
          <Button label="关闭" onClick={() => editSop(null)} />
          {sop.id && !sop.published_version && <Button label="删除草稿" variant="destructive" isDisabled={busy} onClick={() => {
            if (window.confirm('删除 SOP？')) submit('/api/sops/delete', {identifier: sop.id},
              async () => {editSop(null); await refresh();});
          }} />}
        </HStack>
      </VStack>}
    </VStack>
  );
}

export function Executors({entries, onManage}) {
  return <VStack as="section" gap={2}>
    <Heading level={2}>执行器</Heading>
    {entries.length === 0 && <Text>暂无执行器注册</Text>}
    {entries.map(executor => <HStack key={executor.id} gap={2} wrap="wrap" vAlign="center">
      <Text>{executor.name}（{executor.id}） · {executor.online ? '在线' : '离线'} ·
        {executor.enabled ? '已启用' : '已停用'} · {executor.running} 个执行中</Text>
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
