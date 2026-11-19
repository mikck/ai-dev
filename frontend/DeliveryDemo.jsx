import {useState} from 'react';
import {Banner} from '@astryxdesign/core/Banner';
import {Button} from '@astryxdesign/core/Button';
import {Card} from '@astryxdesign/core/Card';
import {Heading} from '@astryxdesign/core/Heading';
import {HStack} from '@astryxdesign/core/HStack';
import {Tab, TabList} from '@astryxdesign/core/TabList';
import {Text} from '@astryxdesign/core/Text';
import {VStack} from '@astryxdesign/core/VStack';

const chapters = [
  {id: 'intake', label: '工单进入', status: 'info', title: '已创建需求 LOOP-12',
    notes: ['影响范围：海外用户导出的订单时间偏差 8 小时。', '验收条件：CSV 时间按用户所在时区展示，覆盖 UTC 和 UTC+08:00。']},
  {id: 'run', label: 'SOP 执行', status: 'info', title: '缺陷修复 SOP 已派发',
    notes: ['需求分析确认时区转换规则。', '前端方案与接口设计分别完成后进入方案评审。', '接口实现完成后进入自动化验收。']},
  {id: 'verification', label: '验收返工', status: 'success', title: '14/14 通过',
    notes: ['第一次验收：2 项测试失败，导出时间仍使用 UTC。', '打回接口实现：统一在导出前转换时区。', '第二次验收：全部测试通过，经人工确认后生成示例 PR #131。']},
  {id: 'evaluation', label: '运行评测', status: 'warning', title: '评测建议',
    notes: ['一次返工暴露了接口实现阶段缺少时区约定。', '角色说明：导出前统一转换为用户所在时区。', '验收规则：显式覆盖 UTC 和 UTC+08:00。']},
];

export function DeliveryDemo({onExit}) {
  const [active, setActive] = useState(chapters[0].id);
  const index = chapters.findIndex(chapter => chapter.id === active);

  return (
    <VStack gap={4}>
      <Card variant="purple" padding={4}>
        <VStack gap={2}>
          <Text type="supporting">产品演示 · 示例数据</Text>
          <Heading level={2}>修复订单导出的时区错误</Heading>
          <Text>从需求进入到交付复盘，跟随同一条工单查看每一步。</Text>
        </VStack>
      </Card>
      <TabList value={active} onChange={setActive} role="tablist" hasDivider>
        {chapters.map(({id, label}) => <Tab key={id} value={id} label={label} panelId={'demo-' + id} />)}
      </TabList>
      {chapters.map(({id, label, status, title, notes}) => (
        <section key={id} id={'demo-' + id} role="tabpanel" aria-label={label} hidden={id !== active}>
          {id === active && <VStack gap={3}>
            <Banner status={status} title={title} />
            <ol style={{listStyle: 'decimal', paddingInlineStart: 'var(--spacing-5)'}}>
              {notes.map(note => <li key={note}><Text>{note}</Text></li>)}
            </ol>
          </VStack>}
        </section>
      ))}
      <HStack gap={2} wrap="wrap">
        <Button label="上一章" isDisabled={index === 0} onClick={() => setActive(chapters[index - 1].id)} />
        {index < chapters.length - 1
          ? <Button label="下一章" variant="primary" onClick={() => setActive(chapters[index + 1].id)} />
          : <Button label="重新观看" variant="primary" onClick={() => setActive(chapters[0].id)} />}
        <Button label="返回需求" variant="ghost" onClick={onExit} />
      </HStack>
    </VStack>
  );
}
