-- Run on isolated snapshot/candidate only; never scan the live gateway database.
BEGIN READ ONLY;
SET LOCAL TIME ZONE 'UTC';
SELECT json_build_object('user_id',user_id,'day',(created_at AT TIME ZONE 'Asia/Shanghai')::date,'rows',count(*),'first',min(created_at),'last',max(created_at),'input_tokens',sum(input_tokens),'output_tokens',sum(output_tokens),'cache_creation_tokens',sum(cache_creation_tokens),'cache_read_tokens',sum(cache_read_tokens),'total_cost',sum(total_cost),'actual_cost',sum(actual_cost),'id_sum',sum(id::numeric),'row_fingerprint',sum(('x'||substr(md5(to_jsonb(u)::text),1,15))::bit(60)::bigint::numeric)) FROM usage_logs u WHERE created_at>='2026-08-31T16:00:00Z' GROUP BY user_id,(created_at AT TIME ZONE 'Asia/Shanghai')::date ORDER BY user_id,(created_at AT TIME ZONE 'Asia/Shanghai')::date;
ROLLBACK;
