import {useMemo, useState} from 'react';
import {Background, Controls, Handle, MarkerType, Position, ReactFlow} from '@xyflow/react';
import {Button} from '@astryxdesign/core/Button';
import {Heading} from '@astryxdesign/core/Heading';
import {HStack} from '@astryxdesign/core/HStack';
import {VStack} from '@astryxdesign/core/VStack';
import '@xyflow/react/dist/style.css';

const horizontalGap = 245;
const verticalGap = 122;

function arrange(nodes) {
  const depths = new Map();
  const occupied = new Map();
  const layout = new Map();
  while (depths.size < nodes.length) {
    const size = depths.size;
    for (const node of nodes) {
      if (depths.has(node.id) || !node.depends.every(id => depths.has(id))) continue;
      const depth = Math.max(0, ...node.depends.map(id => depths.get(id) + 1));
      const row = occupied.get(depth) || 0;
      depths.set(node.id, depth);
      occupied.set(depth, row + 1);
      layout.set(node.id, {x: depth * horizontalGap, y: row * verticalGap});
    }
    if (depths.size === size) break;
  }
  return layout;
}

function wouldCycle(nodes, source, target) {
  const byId = new Map(nodes.map(node => [node.id, node]));
  const pending = [source];
  const seen = new Set();
  while (pending.length) {
    const id = pending.pop();
    if (id === target) return true;
    if (seen.has(id)) continue;
    seen.add(id);
    pending.push(...(byId.get(id)?.depends || []));
  }
  return false;
}

function WorkflowNode({data, selected}) {
  return <section className="sop-canvas-node" data-type={data.type} data-selected={selected}>
    <Handle type="target" position={Position.Left} aria-label="输入端口" />
    <strong title={data.name}>{data.name || '未命名节点'}</strong>
    <small>{data.type === 'review' ? '人工审核' : 'Agent 执行'}{data.publish ? ' · 发布' : ''}</small>
    <Handle type="source" position={Position.Right} aria-label="输出端口" />
  </section>;
}

const nodeTypes = {workflow: WorkflowNode};
const ariaLabelConfig = {
  'controls.ariaLabel': '画布缩放',
  'controls.zoomIn.ariaLabel': '放大',
  'controls.zoomOut.ariaLabel': '缩小',
  'controls.fitView.ariaLabel': '适应画布',
};

export function SOPCanvas({nodes, agents, onChange, children}) {
  const [selection, setSelection] = useState('');
  const [edgeSelection, setEdgeSelection] = useState(null);
  const [notice, setNotice] = useState('');
  const [flow, setFlow] = useState(null);
  const layouts = useMemo(() => arrange(nodes), [nodes]);
  const selected = nodes.find(node => node.id === selection) || nodes[0];
  const selectedIndex = nodes.indexOf(selected);
  const positions = nodes.map((node, index) => node.position || layouts.get(node.id) || {x: 0, y: index * verticalGap});
  const graphNodes = nodes.map((node, index) => ({
    id: node.id, type: 'workflow', data: node, selected: node.id === selected?.id,
    position: positions[index],
    ariaLabel: (node.name || '未命名节点') + '，' + (node.type === 'review' ? '人工审核' : 'Agent 执行'),
  }));
  const edges = nodes.flatMap(node => node.depends.map(source => ({
    id: JSON.stringify([source, node.id]), source, target: node.id, type: 'smoothstep',
    markerEnd: {type: MarkerType.ArrowClosed, color: "var(--console-blue)"},
    selected: edgeSelection?.source === source && edgeSelection?.target === node.id,
  })));

  function choose(id) {
    setSelection(id);
    setEdgeSelection(null);
    setNotice('');
  }

  function update(id, changes) {
    onChange(nodes.map(node => node.id === id ? {...node, ...changes} : node));
  }

  function dependency(source, target, enabled) {
    const node = nodes.find(entry => entry.id === target);
    if (!node || !nodes.some(entry => entry.id === source)) return;
    if (enabled && wouldCycle(nodes, source, target)) {
      setNotice('此连接会形成循环依赖。');
      return;
    }
    const next = enabled ? [...new Set([...node.depends, source])] : node.depends.filter(id => id !== source);
    update(target, {depends: next});
    setNotice('');
    setEdgeSelection(null);
  }

  function add(type) {
    const id = crypto.randomUUID().replaceAll('-', '');
    const publish = false;
    const position = selected
      ? {x: positions[selectedIndex].x + horizontalGap, y: positions[selectedIndex].y}
      : {x: 0, y: 0};
    while (positions.some(other => Math.abs(other.x - position.x) < 198 && Math.abs(other.y - position.y) < 100)) {
      position.y += verticalGap;
    }
    const node = {id, name: type === 'review' ? '人工审核' : 'Agent 节点', type,
      agent: type === 'agent' ? agents[0]?.id || '' : '', prompt: '', depends: [], publish, position};
    onChange([...nodes, node]);
    choose(id);
    requestAnimationFrame(() => flow?.setCenter(position.x + 95, position.y + 40, {zoom: Math.max(.65, flow.getZoom())}));
  }

  function autoArrange() {
    onChange(nodes.map(node => ({...node, position: layouts.get(node.id) || node.position})));
    requestAnimationFrame(() => flow?.fitView({padding: .24}));
  }

  function move(direction) {
    const next = [...nodes];
    const other = selectedIndex + direction;
    [next[selectedIndex], next[other]] = [next[other], next[selectedIndex]];
    // Keep layout positions when only scheduler priority changes.
    onChange(next.map(node => ({...node, position: positions[nodes.indexOf(node)]})));
  }

  function remove() {
    const id = selected.id;
    onChange(nodes.filter(node => node.id !== id)
      .map(node => ({...node, depends: node.depends.filter(parent => parent !== id)})));
    choose('');
  }

  const edge = edges.find(candidate => candidate.selected);
  return <VStack gap={3}>
    <HStack gap={2} wrap="wrap" vAlign="center">
      <Button label="添加 Agent" onClick={() => add('agent')} isDisabled={nodes.length >= 50} />
      <Button label="添加审核" onClick={() => add('review')} isDisabled={nodes.length >= 50} />
      <Button label="自动排列" onClick={autoArrange} isDisabled={!nodes.length} />
      {edge && <Button label="删除连线" onClick={() => dependency(edge.source, edge.target, false)} />}
      {children}
    </HStack>
    <p className="sop-canvas-help">从节点右侧端口拖到下一节点左侧端口。也可在节点配置中选择前置节点。</p>
    {notice && <p role="alert" className="console-error">{notice}</p>}
    <section className="sop-editor-columns">
      <section className="sop-canvas-frame" aria-label="SOP 工作流画布">
        <ReactFlow nodes={graphNodes} edges={edges} nodeTypes={nodeTypes} onInit={setFlow}
          onNodesChange={changes => {
            const moved = new Map(changes.getByType('position').filter(change => change.position)
              .map(change => [change.id, change.position]));
            if (moved.size) onChange(nodes.map(node => moved.has(node.id) ? {...node, position: moved.get(node.id)} : node));
          }}
          onNodeClick={(_, node) => choose(node.id)}
          onSelectionChange={({nodes: picked}) => {
            // Keyboard selection does not dispatch a click.
            if (picked.length === 1 && picked[0].id !== selected?.id) choose(picked[0].id);
          }}
          onEdgeClick={(_, item) => setEdgeSelection({source: item.source, target: item.target})}
          onPaneClick={() => setEdgeSelection(null)}
          onConnect={({source, target}) => dependency(source, target, true)}
          ariaLabelConfig={ariaLabelConfig} fitView fitViewOptions={{padding: .24, maxZoom: 1}}
          minZoom={.14} maxZoom={2} deleteKeyCode={null} zoomOnScroll={false} multiSelectionKeyCode={null}
          panOnDrag>
          <Background gap={20} size={1} />
          <Controls showInteractive={false} />
        </ReactFlow>
        {!nodes.length && <p className="sop-canvas-empty">添加 Agent 或审核节点，开始编排。</p>}
      </section>
      <VStack as="aside" className="sop-node-inspector" gap={3} aria-label="节点配置">
        <Heading level={4}>节点配置</Heading>
        {!selected ? <p className="sop-canvas-help">添加节点后可编辑其配置。</p> : <>
          <label>选中节点 <select value={selected.id} onChange={event => choose(event.target.value)}>
            {nodes.map(node => <option value={node.id} key={node.id}>{node.name || '未命名节点'}</option>)}
          </select></label>
          <label>节点名称 <input value={selected.name} onChange={event => update(selected.id, {name: event.target.value})} /></label>
          <label>节点类型 <select value={selected.type} onChange={event => update(selected.id, {type: event.target.value})}>
            <option value="agent">Agent 执行</option><option value="review">人工审核</option>
          </select></label>
          <label>{selected.type === 'agent' ? '执行说明' : '审核说明'} <textarea value={selected.prompt}
            onChange={event => update(selected.id, {prompt: event.target.value})} /></label>
          {selected.type === 'agent' && <>
            <label>Agent <select value={selected.agent || ''} onChange={event => update(selected.id, {agent: event.target.value})}>
              <option value="">选择 Agent</option>
              {agents.map(agent => <option key={agent.id} value={agent.id}>{agent.name}</option>)}
            </select></label>
            <label>文本产物路径（每行一个）
              <textarea key={selected.id} defaultValue={selected.artifacts?.join('\n')} onChange={event =>
                update(selected.id, {artifacts: event.target.value.split('\n').map(path => path.trim()).filter(Boolean)})} />
            </label>
            <label><input type="checkbox" checked={Boolean(selected.publish)} onChange={event =>
              update(selected.id, {publish: event.target.checked})} /> 推送分支并创建 PR／MR</label>
          </>}
          <fieldset><legend>前置节点</legend>
            {nodes.length <= 1 && <p className="sop-canvas-help">暂无可选节点</p>}
            {nodes.filter(node => node.id !== selected.id).map(node => <label key={node.id}>
              <input type="checkbox" checked={selected.depends.includes(node.id)}
                onChange={event => dependency(node.id, selected.id, event.target.checked)} /> {node.name || '未命名节点'}
            </label>)}
          </fieldset>
          <HStack gap={2} wrap="wrap" vAlign="center">
            <p className="sop-canvas-help">执行优先级 {selectedIndex + 1} / {nodes.length}</p>
            <Button label="提高" size="sm" isDisabled={selectedIndex <= 0} onClick={() => move(-1)} />
            <Button label="降低" size="sm" isDisabled={selectedIndex === nodes.length - 1} onClick={() => move(1)} />
            <Button label="移除节点" size="sm" variant="ghost" onClick={remove} />
          </HStack>
        </>}
      </VStack>
    </section>
  </VStack>;
}
