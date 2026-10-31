import json,pathlib,re,datetime
from html.parser import HTMLParser
import argparse
parser=argparse.ArgumentParser()
parser.add_argument('--evidence-dir',type=pathlib.Path,required=True)
parser.add_argument('--private-raw-dir',type=pathlib.Path,required=True)
args=parser.parse_args()
base=args.evidence_dir
p=json.loads((base/'full.json').read_text())
class Nodes(HTMLParser):
 def __init__(self): super().__init__();self.stack=[];self.messages={};self.cards=set()
 def handle_starttag(self,tag,attrs):
  a=dict(attrs);classes=a.get('class','').split();category=next((c for _,c,_ in reversed(self.stack) if c),'')
  if 'room' in classes: category=a.get('data-roomtype-id','');self.cards.add(category)
  message=('tariff__nodates' in classes) or (self.stack[-1][2] if self.stack else False)
  if tag not in ('img','input','br','meta','link','hr','area','base','embed','param','source','track','wbr'):self.stack.append((tag,category,message))
 def handle_endtag(self,tag):
  for i in range(len(self.stack)-1,-1,-1):
   if self.stack[i][0]==tag: self.stack=self.stack[:i];break
 def handle_data(self,data):
  if self.stack and self.stack[-1][2]:self.messages.setdefault(self.stack[-1][1],[]).append(data)
rows=[]
for e in p['Events']:
 if e['Message']!='bnovo.collect.completed' or e['Values'].get('status')!='no_offers':continue
 v=e['Values'];request=next(r for r in p['Requests'] if r['UID']==v['property_id'] and r['CheckIn']=='-'.join(reversed(v['check_in'].split('-'))) and r['HTTPStatus']==200)
 text=(args.private_raw_dir/(request['SHA256']+'.html')).read_text()
 nodes=Nodes();nodes.feed(text)
 messages={c:' '.join(' '.join(parts).split()) for c,parts in nodes.messages.items() if c}
 expected=3 if v['property_id'].startswith('7f3f') else 10
 assert len(nodes.cards)==expected and len(messages)==expected
 for c,message in messages.items():
  match=re.fullmatch(r'Здесь нет свободных мест с (\d{1,2}) ([а-я]+) (\d{4}) по (\d{1,2}) ([а-я]+) (\d{4})',message)
  assert match
  months={'ноября':11,'декабря':12}
  values=match.groups()
  assert str(datetime.date(int(values[2]),months[values[1]],int(values[0])))==v['check_in']
  assert str(datetime.date(int(values[5]),months[values[4]],int(values[3])))==v['check_out']
 rows.append({'property_id':'arena33' if expected==3 else 'lindsay','check_in':v['check_in'],'check_out':v['check_out'],'cards':sorted(nodes.cards),'no_dates_messages':messages,'plans_js_present':bool(re.search(r'\$plansForJs\s*=',text)), 'plans_js_value':json.JSONDecoder().raw_decode(text[re.search(r'\$plansForJs\s*=',text).end():].lstrip())[0],'select_count':len(re.findall(r'<select\b',text)),'body_sha256':request['SHA256']})
(base/'public-unavailability-evidence.json').write_text(json.dumps(rows,ensure_ascii=False,indent=2)+'\n')
print(rows)
